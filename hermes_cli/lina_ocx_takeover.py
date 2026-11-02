"""``lina models takeover plan`` / ``copy`` (lina#33): what moves from a running OCX into LINA, and the copy.

The old OCX's data dir (its ``OPENCODEX_HOME``) is only ever read. ``plan`` prints the agreed list: what is copied,
what is skipped and why, the listeners the old proxy has (``config.json``'s ``hostname`` / ``port`` /
``unauthenticatedLoopbackListener`` / hub ``managementIngress``, with ``--port`` from the running argv taking
precedence as it does in OCX), the old launchd / systemd job and the secrets that stay in the OS keychain. Secret
values are never printed: secret files are named, keychain references are named, nothing is read out of them.

``copy`` writes ``$LINA_HOME/ocx``: SQLite databases through the SQLite backup API (consistent even while the old
proxy writes, WAL included) and checked with ``PRAGMA integrity_check``; every other file byte-copied and hashed
while it is read. The manifest (path, size, sha256) is written to ``$LINA_HOME/ocx-takeover/manifest.json`` and the
copy is verified against it before it replaces anything. An existing LINA copy is moved aside, never deleted.

Rehearsal and switch live in ``lina_ocx_takeover_rehearse.py`` / ``lina_ocx_takeover_switch.py``; the credit
refresh job in ``lina_ocx_takeover_refresh.py``.
"""

from __future__ import annotations

import fnmatch
import hashlib
import json
import os
import shlex
import shutil
import sqlite3
import time
from dataclasses import asdict, dataclass
from pathlib import Path

from hermes_cli import lina_ocx

OLD_LAUNCHD_LABEL = "com.opencodex.proxy"  # OCX 2.95.0 src/service/state.ts LABEL
OLD_SYSTEMD_UNIT = "opencodex-proxy"  # OCX 2.95.0 src/service/state.ts TASK
KEYCHAIN_SERVICE = "opencodex.provider-api-key.v1"  # OCX 2.95.0 src/providers/api-key-resolve.ts
SECRET_FILES = ("auth.json", "service-api-token", "service-api-token.prev", "admin-api-token")
SQLITE_MAGIC = b"SQLite format 3\x00"

# (glob on the entry name, reason). Matched at every depth; the first match wins.
SKIP_RULES: tuple[tuple[str, str], ...] = (
    ("ocx.pid", "pid of the running proxy"),
    ("*.pid", "pid file"),
    ("runtime-port.json", "port of the running proxy"),
    ("serving-runtimes.json", "runtime state of the running proxy"),
    ("*.lock", "lock file"),
    ("*.sock", "socket"),
    ("crash.log", "crash log"),
    ("backups", "OCX's own backups"),
    ("cache", "cache"),
    (".cache", "cache"),
    ("*-cache", "cache"),
    ("*-wal", "SQLite side file (its database is copied through the backup API)"),
    ("*-shm", "SQLite side file (its database is copied through the backup API)"),
    ("*-journal", "SQLite side file (its database is copied through the backup API)"),
    (".config.json.lina-tmp", "LINA's temp file"),
)


class TakeoverError(lina_ocx.OcxError):
    pass


@dataclass(frozen=True)
class Listener:
    role: str  # "main" (the proxy port), "loopback" (unauthenticated loopback listener), "management"
    host: str
    port: int
    loopback: bool
    token_required: bool


def takeover_dir(root: Path) -> Path:
    return root / "ocx-takeover"


def manifest_path(root: Path) -> Path:
    return takeover_dir(root) / "manifest.json"


def skip_reason(name: str) -> str | None:
    for pattern, reason in SKIP_RULES:
        if fnmatch.fnmatchcase(name, pattern):
            return reason
    return None


def is_sqlite(path: Path) -> bool:
    try:
        with open(path, "rb") as fh:
            return fh.read(len(SQLITE_MAGIC)) == SQLITE_MAGIC
    except OSError:
        return False


def is_loopback_host(host: str | None) -> bool:
    """OCX 2.95.0's ``isLoopbackHostname``: only these names are loopback; any other bind needs the API token."""
    normalized = (host if host is not None else "127.0.0.1").strip().lower().rstrip(".")
    return normalized in {"", "localhost", "127.0.0.1", "::1", "[::1]"}


def read_config(source: Path) -> dict:
    path = source / "config.json"
    if not path.exists():
        return {}
    try:
        config = json.loads(path.read_text(encoding="utf-8-sig"))
    except ValueError as exc:
        raise TakeoverError(f"{path} is not JSON ({exc})") from exc
    if not isinstance(config, dict):
        raise TakeoverError(f"{path} is not a JSON object")
    return config


def argv_port(argv: str | None) -> int | None:
    """``--port N`` / ``--port=N`` of the running proxy's command line, which OCX prefers over ``config.port``."""
    words = shlex.split(argv or "")
    for i, word in enumerate(words):
        if word == "--port" and i + 1 < len(words):
            return int(words[i + 1])
        if word.startswith("--port="):
            return int(word.split("=", 1)[1])
    return None


def listeners(config: dict, argv: str | None = None) -> list[Listener]:
    port = argv_port(argv) or int(config.get("port") or lina_ocx.DEFAULT_PORT)
    host = str(config.get("hostname") or "127.0.0.1")
    main_loopback = is_loopback_host(host)
    found = [Listener("main", host, port, main_loopback, not main_loopback)]
    extra = config.get("unauthenticatedLoopbackListener")
    if isinstance(extra, dict) and extra.get("enabled") is True:
        found.append(Listener("loopback", "127.0.0.1", int(extra.get("port") or port), True, False))
    hub = config.get("hub") if isinstance(config.get("hub"), dict) else {}
    ingress = hub.get("managementIngress")
    if config.get("runtimeRole") == "hub" and isinstance(ingress, dict) and ingress.get("enabled") is True:
        found.append(Listener("management", "127.0.0.1", int(ingress["port"]), True, False))
    return found


def keychain_refs(value) -> list[str]:
    """``keychain:<provider>`` / ``keychain:<provider>/<pool id>`` references anywhere in the config (names only)."""
    if isinstance(value, str):
        return [value] if value.startswith("keychain:") else []
    items = value.values() if isinstance(value, dict) else value if isinstance(value, list) else ()
    return sorted({ref for item in items for ref in keychain_refs(item)})


def old_job(job: str | None = None) -> dict:
    """The old OCX's own service (``ocx service install``): launchd ``com.opencodex.proxy`` / systemd user unit
    ``opencodex-proxy``, or *job* when given. Only its definition file is looked at."""
    from hermes_cli.lina_ocx_service import backend
    kind = backend()
    if kind == "launchd":
        from hermes_constants import get_real_home
        name = job or OLD_LAUNCHD_LABEL
        path = Path(get_real_home()) / "Library" / "LaunchAgents" / f"{name}.plist"
    else:
        from hermes_cli.gateway import user_systemd_unit_dir
        name = (job or OLD_SYSTEMD_UNIT).removesuffix(".service")
        path = user_systemd_unit_dir() / f"{name}.service"
    return {"kind": kind, "name": name, "path": str(path), "found": path.exists()}


def _tree_size(path: Path) -> tuple[int, int]:
    files = size = 0
    for dirpath, _dirs, names in os.walk(path):
        for name in names:
            if skip_reason(name) is None:
                files += 1
                size += os.lstat(os.path.join(dirpath, name)).st_size
    return files, size


def inventory(source: Path) -> list[dict]:
    """Top-level entries of the data dir with what happens to each: copy (file / sqlite / dir) or skip (why)."""
    rows = []
    for entry in sorted(source.iterdir(), key=lambda p: p.name):
        reason = skip_reason(entry.name)
        if reason is not None:
            rows.append({"name": entry.name, "action": "skip", "why": reason})
        elif entry.is_dir() and not entry.is_symlink():
            files, size = _tree_size(entry)
            rows.append({"name": entry.name + "/", "action": "copy", "kind": "dir", "files": files, "size": size})
        else:
            kind = "sqlite" if is_sqlite(entry) else "secret" if entry.name in SECRET_FILES else "file"
            rows.append({"name": entry.name, "action": "copy", "kind": kind, "size": os.lstat(entry).st_size})
    return rows


def build_plan(source: Path, argv: str | None = None, job: str | None = None) -> dict:
    if not (source / "config.json").is_file():
        raise TakeoverError(f"{source} has no config.json; is it an OCX data dir (OPENCODEX_HOME)?")
    config = read_config(source)
    return {"source": str(source), "entries": inventory(source),
            "listeners": [asdict(item) for item in listeners(config, argv)], "old_job": old_job(job),
            "keychain": {"service": KEYCHAIN_SERVICE, "refs": keychain_refs(config)}}


def _human(size: int) -> str:
    for unit in ("B", "KB", "MB", "GB"):
        if size < 1024 or unit == "GB":
            return f"{size:.0f} {unit}" if unit == "B" else f"{size:.1f} {unit}"
        size /= 1024
    return str(size)


def _entry_line(row: dict) -> str:
    if row["action"] == "skip":
        return f"  skip  {row['name']}  ({row['why']})"
    what = {"sqlite": "SQLite, backup API + integrity check", "secret": "secret, value not shown",
            "dir": f"{row.get('files', 0)} files", "file": "bytes + sha256"}[row["kind"]]
    return f"  copy  {row['name']}  {_human(row['size'])}  ({what})"


def render_plan(plan: dict, dest: Path) -> list[str]:
    lines = [f"From {plan['source']} (read only) to {dest}", "Files:"]
    lines += [_entry_line(row) for row in plan["entries"]]
    lines.append("Listeners (kept on the same addresses and ports by `takeover switch`):")
    for item in plan["listeners"]:
        auth = "API token required" if item["token_required"] else "no token (loopback)"
        lines.append(f"  {item['role']:<10} {item['host']}:{item['port']}  "
                     f"{'loopback' if item['loopback'] else 'not loopback'}, {auth}")
    job = plan["old_job"]
    lines.append(f"Old job: {job['kind']} {job['name']} ({job['path']}{'' if job['found'] else ', not found'})")
    refs = plan["keychain"]["refs"]
    lines.append(f"Stay in the OS keychain (service {plan['keychain']['service']}, names only): "
                 + (", ".join(refs) if refs else "none referenced in config.json"))
    return lines


def plan(source: Path, argv: str | None = None, job: str | None = None, as_json: bool = False,
         root: Path | None = None, out=print) -> int:
    root = root or lina_ocx.lina_root()
    data = build_plan(source.expanduser().resolve(), argv, job)
    for line in [json.dumps(data, indent=2)] if as_json else render_plan(data, lina_ocx.data_dir(root)):
        out(line)
    return 0


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _copy_bytes(src: Path, dst: Path) -> str:
    """Byte copy hashed while read, so the manifest names exactly what was written; mode and mtimes kept."""
    digest = hashlib.sha256()
    with open(src, "rb") as fin, open(dst, "xb") as fout:  # windows-footgun: ok — binary
        for chunk in iter(lambda: fin.read(1 << 20), b""):
            digest.update(chunk)
            fout.write(chunk)
    shutil.copystat(src, dst)
    return digest.hexdigest()


def _copy_sqlite(src: Path, dst: Path) -> str:
    """SQLite backup API from a read-only connection: one consistent snapshot, WAL content included.

    With no ``-wal`` file nobody has the database open, and a read-only open of a WAL database would create an
    empty ``-wal`` / ``-shm`` beside the source; ``immutable=1`` reads it without creating anything."""
    quiet = not Path(f"{src}-wal").exists()
    source = sqlite3.connect(f"{src.resolve().as_uri()}?mode=ro{'&immutable=1' if quiet else ''}", uri=True)
    target = sqlite3.connect(dst)
    try:
        source.backup(target)
        result = target.execute("PRAGMA integrity_check").fetchone()[0]
        target.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    finally:
        target.close()
        source.close()
    if result != "ok":
        raise TakeoverError(f"{src.name}: integrity_check of the copy says {result!r}")
    for side in ("-wal", "-shm"):
        Path(f"{dst}{side}").unlink(missing_ok=True)
    return _sha256(dst)


def _copy_tree(source: Path, staging: Path) -> list[dict]:
    files = []
    for dirpath, dirnames, names in os.walk(source):
        here = Path(dirpath)
        dirnames[:] = sorted(d for d in dirnames if skip_reason(d) is None and not (here / d).is_symlink())
        target_dir = staging / here.relative_to(source)
        target_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        for name in sorted(names):
            src = here / name
            if skip_reason(name) is not None or src.is_symlink() or not src.is_file():
                continue
            dst = target_dir / name
            kind = "sqlite" if is_sqlite(src) else "file"
            digest = _copy_sqlite(src, dst) if kind == "sqlite" else _copy_bytes(src, dst)
            files.append({"path": str(dst.relative_to(staging)), "size": dst.stat().st_size, "sha256": digest,
                          "kind": kind})
    return files


def verify(data: Path, manifest: dict) -> list[str]:
    """Paths of *data* that do not match the manifest (missing, other size or other sha256)."""
    bad = []
    for item in manifest["files"]:
        path = data / item["path"]
        if not path.is_file() or path.stat().st_size != item["size"] or _sha256(path) != item["sha256"]:
            bad.append(item["path"])
    return bad


def _has_data(path: Path) -> bool:
    return path.is_dir() and any(path.iterdir())


def _move_aside(path: Path) -> Path:
    aside = path.with_name(f"{path.name}.replaced-{time.strftime('%Y%m%d-%H%M%S')}")
    while aside.exists():
        aside = aside.with_name(aside.name + "-1")
    os.replace(path, aside)
    return aside


def copy(source: Path, root: Path | None = None, replace: bool = False, out=print) -> int:
    root = root or lina_ocx.lina_root()
    source = source.expanduser().resolve()
    dest = lina_ocx.data_dir(root)
    if not (source / "config.json").is_file():
        out(f"takeover: {source} has no config.json; is it an OCX data dir (OPENCODEX_HOME)?")
        return 1
    if dest.resolve() == source:
        out(f"takeover: {source} is LINA's own data dir")
        return 1
    if _has_data(dest) and not replace:
        out(f"takeover: {dest} already has data; pass --replace to move it aside (it is never deleted)")
        return 1
    if _has_data(dest) and lina_ocx.own_proxy_running(root, lina_ocx.configured_port(root)):
        out(f"takeover: LINA's OCX is running on {dest}; stop it first (`lina services stop ocx`)")
        return 1
    staging = root / ".ocx.takeover-partial"
    shutil.rmtree(staging, ignore_errors=True)
    try:
        files = _copy_tree(source, staging)
        manifest = {"source": str(source), "created": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "files": files}
        bad = verify(staging, manifest)
        if bad:
            raise TakeoverError(f"the copy does not match its manifest: {', '.join(bad[:5])}")
        if dest.exists():
            aside = _move_aside(dest) if _has_data(dest) else None
            if aside is None:
                dest.rmdir()
            else:
                out(f"  previous LINA copy moved aside to {aside}")
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    takeover_dir(root).mkdir(mode=0o700, parents=True, exist_ok=True)
    manifest_path(root).write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    sqlite_count = sum(1 for item in files if item["kind"] == "sqlite")
    out(f"✓ copied {len(files)} files ({sqlite_count} SQLite through the backup API, integrity ok) "
        f"from {source} to {dest}; manifest {manifest_path(root)} verified")
    return 0


def run_copy(source: Path, replace: bool = False, out=print) -> int:
    """``lina models takeover copy``: the copy, then the credit refresh cron entry (created disabled)."""
    from hermes_cli import lina_ocx_takeover_refresh
    code = copy(source, replace=replace, out=out)
    if code == 0:
        lina_ocx_takeover_refresh.ensure_job(lina_ocx.lina_root(), out=out)
    return code
