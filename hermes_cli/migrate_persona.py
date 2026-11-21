"""Read legacy personas and publish a reviewed split into a destination install."""

from __future__ import annotations

import json
import re
from argparse import Namespace
from collections.abc import Iterator
from contextlib import contextmanager, nullcontext
from pathlib import Path

from hermes_cli.active_sessions import _FileLock
from hermes_cli.migrate_memory_copy import _safe
from hermes_cli.persona_split import split_personas
from hermes_constants import COMMON_PERSONA_FILE, PROFILE_ID_RE
from utils import atomic_write_bytes


def _input_path(value: str, root: Path) -> Path:
    path = _safe(Path(value).expanduser())
    if not path.is_file() or path.is_relative_to(root):
        raise ValueError(f"expected a regular input file outside the destination: {path}")
    return path


def _sources(specs: list[str], root: Path) -> dict[str, str]:
    sources = {}
    seen = set()
    for spec in specs:
        name, sep, value = spec.partition("=")
        if not sep or not PROFILE_ID_RE.fullmatch(name) or name in sources:
            raise ValueError("--source requires a unique profile name followed by =FILE")
        path = _input_path(value, root)
        stat = path.stat()
        identity = (stat.st_dev, stat.st_ino)
        if identity in seen:
            raise ValueError(f"source file supplied more than once: {path}")
        seen.add(identity)
        sources[name] = path.read_bytes().decode("utf-8")
    return sources


def _drops(value: str | None, root: Path) -> dict[str, str]:
    drops = {}
    if value:
        for number, line in enumerate(_input_path(value, root).read_text(encoding="utf-8-sig").splitlines(), 1):
            if not line.strip():
                continue
            digest, sep, reason = line.partition(":")
            if not sep or not re.fullmatch("[0-9a-f]{64}", digest) or not reason.strip() or digest in drops:
                raise ValueError(f"invalid or repeated sha256:reason at drop file line {number}")
            drops[digest] = reason.strip()
    return drops


def _json_line(value) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True) + "\n"


def _persona_lock(root: Path) -> _FileLock:
    return _FileLock(_safe(root / "shared" / ".persona-migration.lock"))


@contextmanager
def clone_persona_membership(source: Path | None, destination: Path) -> Iterator[None]:
    """Publish the clone with its source's ordered references, rolling back a failed rename."""
    root = destination.parent.parent
    path = root / COMMON_PERSONA_FILE
    if source is None or not path.exists():
        yield
        return

    with _persona_lock(root):
        original = _safe(path).read_bytes()
        common = json.loads(original)
        homes = common["homes"]
        name = destination.name
        source_name = "default" if source == root else source.name
        if homes.get(source_name) == homes.get(name):
            yield
            return
        if destination.exists():
            raise FileExistsError(f"Profile '{name}' already exists")
        if source_name in homes:
            homes[name] = homes[source_name]
        else:
            del homes[name]
        atomic_write_bytes(path, _json_line(common).encode(), mode=0o600)
        try:
            yield
        except BaseException:
            atomic_write_bytes(path, original, mode=0o600)
            raise


def _migrate(args: Namespace) -> dict:
    root = _safe(Path(args.home).expanduser())
    sources = _sources(args.source, root)
    split = split_personas(sources, _drops(args.drop, root))
    # Preflight every output before replacing even an unedited destination copy.
    # A completed or interrupted apply can be repeated with the same inputs.
    outputs = {root / COMMON_PERSONA_FILE: _json_line(split.common).encode()}
    originals = {}
    for name, persona in split.personas.items():
        home = root if name == "default" else root / "profiles" / name
        path = home / "SOUL.md"
        originals[path] = sources[name].encode()
        outputs[path] = persona.encode()
        rows = (_json_line(row) for row in split.ledger if row["home"] == name)
        outputs[home / "persona-ledger.jsonl"] = "".join(rows).encode()

    lock = _persona_lock(root) if args.apply else nullcontext()
    with lock:
        pending = {}
        for path, data in outputs.items():
            _safe(path)
            if path.exists():
                current = path.read_bytes()
                if current == data:
                    continue
                if current != originals.get(path):
                    raise ValueError(f"destination contains different data: {path}")
            pending[path] = data
        if args.apply:
            for path, data in pending.items():
                atomic_write_bytes(path, data, mode=0o600)
    return {"ok": True, "applied": args.apply, "summary": split.summary, "ledger": split.ledger}


def run(args: Namespace) -> int:
    try:
        report = _migrate(args)
    except (OSError, ValueError, RuntimeError) as exc:
        report = {"ok": False, "error": str(exc)}
    print(json.dumps(report, ensure_ascii=False))
    return 0 if report["ok"] else 1
