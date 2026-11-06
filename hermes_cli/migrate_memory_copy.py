"""Copy-only migration inputs. Consumers must use Snapshot paths, never live inputs.

Config is deliberately reduced to known scalar memory settings, not a runnable
clone. Unknown files, .env, auth material and plugin configuration are excluded.
The orchestrator owns copying profile_dir to a new destination such as alpha-copy.
"""

from __future__ import annotations

import hashlib
import json
import re
import sqlite3
from contextlib import closing
from dataclasses import asdict, dataclass
from pathlib import Path
from tempfile import TemporaryDirectory
from typing import Final

from hermes_cli.config_backend import FileBackend


_MEMORY_BOOLS: Final = (
    "memory_enabled", "user_profile_enabled", "prefetch_spill_enabled",
    "write_approval", "auto_recall_append",
)
_MEMORY_INTS: Final = ("memory_char_limit", "user_char_limit", "nudge_interval")


class SnapshotError(ValueError):
    """An input or an existing snapshot cannot be safely used."""

    def __init__(self, path: Path, reason: str) -> None:
        self.path = path
        self.reason = reason
        super().__init__(f"{path}: {reason}")


@dataclass(frozen=True, slots=True)
class CopyReceipt:
    source: Path
    destination: Path
    method: str
    source_sha256: str
    sha256: str
    size: int
    consistency: str


@dataclass(frozen=True, slots=True)
class Snapshot:
    root: Path
    source_home: Path
    source_profile_dir: Path
    profile: str
    profile_dir: Path
    state_db: Path
    lcm_db: Path | None
    work_db: Path | None
    honcho_dump: Path | None
    spool: Path | None
    receipt: tuple[CopyReceipt, ...]


def _safe(path: Path) -> Path:
    """Reject traversal and symlinks before resolving any filesystem input."""
    if ".." in path.parts:
        raise SnapshotError(path, "path traversal is forbidden")
    absolute = path.absolute()
    for part in (absolute, *absolute.parents):
        if part.is_symlink():
            raise SnapshotError(part, "symlinks are forbidden")
    return absolute


def _digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def _fingerprint(path: Path, database: bool) -> str:
    """Include WAL bytes and filesystem identity in source provenance."""
    records = []
    for candidate in (path, Path(str(path) + "-wal")) if database else (path,):
        _safe(candidate)
        if candidate.exists():
            if not candidate.is_file():
                raise SnapshotError(candidate, "expected a regular file")
            stat = candidate.stat()
            records.append((str(candidate), stat.st_dev, stat.st_ino, _digest(candidate)))
    return hashlib.sha256(json.dumps(records).encode()).hexdigest()


def _inventory(profile_dir: Path) -> list[Path]:
    """Only known memory/persona files and the skills tree are implicit inputs."""
    paths = []
    for relative in ("SOUL.md", "persona.md", "MEMORY.md", "USER.md",
                     "memory/MEMORY.md", "memory/USER.md", "config.yaml"):
        path = _safe(profile_dir / relative)
        if path.exists():
            if not path.is_file():
                raise SnapshotError(path, "expected a regular file")
            paths.append(path)
    for directory_name in ("skills", "memories", "memory", "personas"):
        skills = _safe(profile_dir / directory_name)
        if not skills.exists():
            continue
        def visit(directory: Path) -> None:
            for path in sorted(directory.iterdir()):
                _safe(path)
                if path.is_dir():
                    visit(path)
                elif path.is_file():
                    if path not in paths:
                        paths.append(path)
                else:
                    raise SnapshotError(path, "expected a regular file or directory")
        visit(skills)
    return paths


def _config_bytes(home: Path) -> bytes:
    """Read the on-disk backend explicitly, without defaults, overlays or env expansion."""
    doc = FileBackend().read_user_doc(home) or {}
    if not isinstance(doc, dict) or not isinstance(doc.get("memory", {}), dict):
        raise SnapshotError(home / "config.yaml", "expected a config mapping")
    memory = doc.get("memory", {})
    safe = {key: memory[key] for key in _MEMORY_BOOLS if type(memory.get(key)) is bool}
    safe.update({key: memory[key] for key in _MEMORY_INTS if type(memory.get(key)) is int})
    # JSON is a YAML subset; no interpolations or arbitrary tags survive.
    return (json.dumps({"memory": safe}, sort_keys=True, indent=2) + "\n").encode()


def _honcho_identity_bytes(path: Path, profile: str) -> bytes:
    """Retain source selection, not transport credentials or another profile's host."""
    from plugins.memory.honcho.client import _OBSERVATION_MODES, _normalize_choice, profile_host_key

    raw = json.loads(path.read_text(encoding="utf-8-sig"))
    host = profile_host_key(profile)
    block = raw.get("hosts", {}).get(host)
    if not isinstance(block, dict):
        raise SnapshotError(path, "selected profile has no Honcho host block")
    identity = {field: block.get(field) for field in ("workspace", "aiPeer", "peerName")}
    if any(not isinstance(value, str) or not value for value in identity.values()):
        raise SnapshotError(path, "Honcho host requires workspace, aiPeer and peerName")
    mode = _normalize_choice(block.get("observationMode") or raw.get("observationMode") or "unified",
                             _OBSERVATION_MODES)
    identity["observationMode"] = mode
    return (json.dumps({"hosts": {host: identity}}, sort_keys=True, indent=2) + "\n").encode()


def _copied_file_bytes(path: Path, relative: Path, profile_dir: Path, profile: str) -> bytes:
    if relative == Path("profile/config.yaml"):
        return _config_bytes(profile_dir)
    if relative == Path("external/honcho-config.json"):
        return _honcho_identity_bytes(path, profile)
    return path.read_bytes()


def snapshot(
    source_home: Path, profile: str, work_dir: Path, *,
    honcho_dump: Path | None = None, spool: Path | None = None,
    work_db: Path | None = None,
) -> Snapshot:
    """Atomically publish work_dir/snapshot, or verify and reuse it unchanged.

    state.db is required. lcm.db and profile-local work.db are optional. Explicit
    inputs may live outside source_home, but must be regular, non-symlink files.
    A repeat verifies snapshot bytes and request provenance without reading live
    input contents. Database backups are individually consistent, not a shared
    cross-database epoch. Use a fresh work_dir to capture newer live inputs.
    """
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", profile):
        raise SnapshotError(Path(profile), "invalid profile name")
    source_home, work_dir = _safe(source_home), _safe(work_dir)
    profile_dir = _safe(source_home if profile == "default" else source_home / "profiles" / profile)
    if work_dir.is_relative_to(source_home) or source_home.is_relative_to(work_dir):
        raise SnapshotError(work_dir, "source and work directory overlap")
    root = _safe(work_dir / "snapshot")
    request = json.dumps([str(source_home), str(profile_dir), profile,
                          *[str(_safe(path)) if path is not None else None
                            for path in (honcho_dump, spool, work_db)]])
    if root.exists():
        copies = _reuse(root, request)
        return _result(root, source_home, profile_dir, profile, copies)
    if not profile_dir.is_dir():
        raise SnapshotError(profile_dir, "source profile does not exist")
    # Each tuple is (source, relative destination, online SQLite backup).
    inputs = [(path, Path("profile") / path.relative_to(profile_dir), False)
              for path in _inventory(profile_dir)]
    honcho_config = _safe(profile_dir / "honcho.json")
    if honcho_config.exists():
        inputs.append((honcho_config, Path("external/honcho-config.json"), False))
    state = _safe(profile_dir / "state.db")
    inputs.append((state, Path("profile/state.db"), True))
    lcm = _safe(profile_dir / "lcm.db")
    if lcm.exists():
        inputs.append((lcm, Path("profile/lcm.db"), True))
    selected_work = _safe(work_db) if work_db is not None else _safe(profile_dir / "work.db")
    if work_db is not None or selected_work.exists():
        inputs.append((selected_work, Path("profile/work.db"), True))
    for path, name in ((honcho_dump, "honcho.dump"), (spool, "events.jsonl")):
        if path is not None:
            inputs.append((_safe(path), Path("external") / name, False))
    for path, _, database in inputs:
        if not path.is_file():
            raise SnapshotError(path, "required input is not a regular file")
        if path.is_relative_to(work_dir):
            raise SnapshotError(path, "input and work directory overlap")
        if database:
            _safe(Path(str(path) + "-wal"))
            _safe(Path(str(path) + "-shm"))
    provenance = [(str(path), str(relative), database, "" if database else _fingerprint(path, False))
                  for path, relative, database in inputs]

    def receipts(base: Path) -> tuple[CopyReceipt, ...]:
        return tuple(CopyReceipt(path, root / relative,
                                 "sqlite-backup" if database else
                                 "sanitized-config" if relative.name in {"config.yaml", "honcho-config.json"} else "file",
                                 "" if database else _digest(path), _digest(base / relative),
                                 (base / relative).stat().st_size,
                                 "online-backup" if database else "unchanged-file")
                     for index, (path, relative, database) in enumerate(inputs))

    def manifest(copies: tuple[CopyReceipt, ...]) -> str:
        return request + "\n" + json.dumps([asdict(row) for row in copies], default=str, sort_keys=True) + "\n"

    work_dir.mkdir(parents=True, exist_ok=True)
    with TemporaryDirectory(prefix=".snapshot-", dir=work_dir) as temporary:
        staging = Path(temporary) / "snapshot"
        staging.mkdir()
        for path, relative, database in inputs:
            destination = staging / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            if database:
                with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as source:
                    with closing(sqlite3.connect(destination)) as target:
                        source.backup(target)
                        # A sealed copy has no concurrent writer. Persist DELETE mode
                        # so even mode=ro consumers cannot create WAL/SHM sidecars.
                        target.execute("PRAGMA journal_mode=DELETE")
            else:
                destination.write_bytes(_copied_file_bytes(path, relative, profile_dir, profile))
        if any(not database and _fingerprint(path, False) != provenance[index][3]
               for index, (path, _, database) in enumerate(inputs)):
            raise SnapshotError(profile_dir, "file source changed during snapshot; retry with stable files")
        copies = receipts(staging)
        (staging / "receipt.json").write_text(manifest(copies))
        for path in staging.rglob("*"):
            if path.is_file():
                path.chmod(0o444)
        staging.rename(root)
    return _result(root, source_home, profile_dir, profile, copies)


def _reuse(root: Path, request: str) -> tuple[CopyReceipt, ...]:
    """Verify copies only; the live source may have advanced or been removed."""
    receipt_path = _safe(root / "receipt.json")
    lines = receipt_path.read_text().splitlines()
    if len(lines) != 2 or lines[0] != request:
        raise SnapshotError(root, "source request provenance differs; refusing overwrite")
    rows = json.loads(lines[1])
    copies = tuple(CopyReceipt(Path(row["source"]), Path(row["destination"]), row["method"],
                               row["source_sha256"], row["sha256"], row["size"], row["consistency"])
                   for row in rows)
    expected = {receipt_path}
    for row in copies:
        path = _safe(row.destination)
        if not path.is_relative_to(root) or not path.is_file():
            raise SnapshotError(path, "invalid snapshot destination; refusing overwrite")
        if path.stat().st_size != row.size or _digest(path) != row.sha256:
            raise SnapshotError(path, "snapshot digest differs; refusing overwrite")
        expected.add(path)
    actual = set()
    for path in root.rglob("*"):
        _safe(path)
        if not path.is_dir():
            actual.add(path)
    if actual != expected:
        raise SnapshotError(root, "snapshot file inventory differs; refusing overwrite")
    return copies


def _result(root: Path, source_home: Path, profile_dir: Path, profile: str,
            copies: tuple[CopyReceipt, ...]) -> Snapshot:
    copied = {row.destination.relative_to(root) for row in copies}
    return Snapshot(root, source_home, profile_dir, profile, root / "profile",
                    root / "profile/state.db",
                    root / "profile/lcm.db" if Path("profile/lcm.db") in copied else None,
                    root / "profile/work.db" if Path("profile/work.db") in copied else None,
                    root / "external/honcho.dump" if Path("external/honcho.dump") in copied else None,
                    root / "external/events.jsonl" if Path("external/events.jsonl") in copied else None, copies)
