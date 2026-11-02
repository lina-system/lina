"""LINA's own pinned OCX (lina#32): the pin, where it lives in the LINA home, and installing it.

``pm/lina-deps.json`` pins one Node (>= 24, per-platform sha256) and one OCX version (its npm integrity). Everything
lands under the LINA root and nothing is installed globally:

* ``runtime/node-<ver>/``: Node, unpacked only after its tarball matched the pinned sha256.
* ``vendor/ocx/<ver>/``: OCX installed by that Node's npm with ``--prefix``; its ``package-lock.json`` must name
  the pinned version and integrity. ``vendor/ocx/current`` points at the active version.
* ``vendor/ocx/home/``: the ``HOME`` OCX runs with. OCX writes ``$HOME/.opencodex/ocx-homes`` and
  ``$HOME/.claude/agents`` even when ``OPENCODEX_HOME`` is set, so it never sees the account's home.
* ``ocx/``: OCX's data dir (``OPENCODEX_HOME``).

The proxy (``lina_ocx_service.py``) listens on ``ocx.port`` of the LINA root's ``config.yaml`` (default 10100, the
provider's default). OCX's CLI finds its proxy through the data dir and falls back to port 10100, which may be
another OCX on this account, so ``own_proxy_running`` is asked before any client verb.
"""

from __future__ import annotations

import contextlib
import hashlib
import json
import os
import shutil
import socket
import subprocess
import sys
import tarfile
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

PIN_FILE = Path(__file__).resolve().parents[1] / "pm" / "lina-deps.json"
LOCK_DIR = PIN_FILE.parent / "npm-locks"
DEFAULT_PORT = 10100
_ARCH = {"amd64": "x64", "arm64": "arm64"}
_OS = {"darwin": "darwin", "linux": "linux"}


class OcxError(Exception):
    pass


@dataclass(frozen=True)
class Pins:
    node_version: str
    node_dist: str
    node_sha256: dict[str, str]
    package: str
    version: str
    integrity: str


def pins(path: Path = PIN_FILE) -> Pins:
    data = json.loads(path.read_text(encoding="utf-8-sig"))
    node, ocx = data["node"], data["ocx"]
    return Pins(node["version"], node["dist"], dict(node["sha256"]), ocx["package"], ocx["version"],
                ocx["integrity"])


def lina_root() -> Path:
    from hermes_constants import get_default_hermes_root
    return get_default_hermes_root()


def node_dir(root: Path, version: str) -> Path:
    return root / "runtime" / f"node-{version}"


def vendor_dir(root: Path) -> Path:
    return root / "vendor" / "ocx"


def version_dir(root: Path, version: str) -> Path:
    return vendor_dir(root) / version


def data_dir(root: Path) -> Path:
    return root / "ocx"


def backup_dir(root: Path) -> Path:
    return root / "ocx.backup"


def home_dir(root: Path) -> Path:
    return vendor_dir(root) / "home"


def current_version(root: Path, vendor: Path | None = None) -> str | None:
    link = (vendor or vendor_dir(root)) / "current"
    return os.readlink(link) if link.is_symlink() else None


def set_current(root: Path, version: str, vendor: Path | None = None) -> None:
    """Point ``<vendor>/current`` (default ``vendor/ocx``) at *version* in one rename, so a reader never sees it
    missing."""
    link = (vendor or vendor_dir(root)) / "current"
    tmp = link.with_name(".current.tmp")
    tmp.unlink(missing_ok=True)
    os.symlink(version, tmp)
    os.replace(tmp, link)


def platform_key() -> str:
    from hermes_platform.host.facts import native_arch
    system, arch = _OS.get(sys.platform), _ARCH.get(native_arch())
    if not system or not arch:
        raise OcxError(f"no pinned Node for {sys.platform}/{native_arch()}")
    return f"{system}-{arch}"


def download(url: str, dest: Path) -> None:
    try:
        with urllib.request.urlopen(url, timeout=120) as response, open(dest, "wb") as fh:
            shutil.copyfileobj(response, fh)
    except (urllib.error.URLError, OSError) as exc:
        raise OcxError(f"download of {url} failed: {exc}") from exc


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def install_node(root: Path, pin: Pins, fetch: Callable[[str, Path], None] = download) -> Path:
    """The pinned Node under ``runtime/``; the tarball is checked against the pin before anything is unpacked."""
    dest = node_dir(root, pin.node_version)
    if (dest / "bin" / "node").exists():
        return dest
    key = platform_key()
    name = f"node-v{pin.node_version}-{key}.tar.gz"
    work = root / "runtime" / f".node-{pin.node_version}.partial"
    shutil.rmtree(work, ignore_errors=True)
    work.mkdir(parents=True)
    try:
        tarball = work / name
        fetch(f"{pin.node_dist}/v{pin.node_version}/{name}", tarball)
        got = _sha256(tarball)
        if got != pin.node_sha256[key]:
            raise OcxError(f"checksum mismatch for {name}: expected {pin.node_sha256[key]}, got {got}; nothing unpacked")
        with tarfile.open(tarball) as archive:
            archive.extractall(work / "x", filter="data")
        (top,) = (work / "x").iterdir()
        os.replace(top, dest)
    finally:
        shutil.rmtree(work, ignore_errors=True)
    return dest


def npm_env(root: Path, node: Path) -> dict[str, str]:
    home = home_dir(root)
    home.mkdir(parents=True, exist_ok=True)
    return {"HOME": str(home), "PATH": os.pathsep.join([str(node / "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"]),
            "npm_config_cache": str(root / "runtime" / "npm-cache"), "npm_config_update_notifier": "false",
            "npm_config_fund": "false", "npm_config_audit": "false"}


def _locked(prefix: Path, package: str) -> dict:
    lock = prefix / "package-lock.json"
    if not lock.exists():
        return {}
    return json.loads(lock.read_text(encoding="utf-8-sig")).get("packages", {}).get(f"node_modules/{package}", {})


def npm(root: Path, node: Path, *args: str, cwd: Path | None = None, label: str = "",
        run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> None:
    """The pinned Node's npm with LINA's HOME and cache (``npm_env``); a failure raises with npm's last line."""
    npm_cli = node / "lib" / "node_modules" / "npm" / "bin" / "npm-cli.js"
    result = run([str(node / "bin" / "node"), str(npm_cli), *args], env=npm_env(root, node), cwd=cwd,
                 capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=900, check=False)
    if result.returncode != 0:
        tail = (result.stderr or result.stdout or "").strip().splitlines()[-1:] or ["no output"]
        raise OcxError(f"{label or 'npm ' + ' '.join(args[:2])} failed: {tail[0]}")


def committed_lock(name: str, package: str, version: str) -> Path | None:
    """``pm/npm-locks/<name>`` when it locks *package* @ *version*: the whole tree's integrity, reviewed in git."""
    lock = LOCK_DIR / name
    try:
        wanted = json.loads((lock / "package.json").read_text(encoding="utf-8-sig"))["dependencies"][package]
    except (OSError, ValueError, KeyError):
        return None
    return lock if wanted == version and (lock / "package-lock.json").exists() else None


def _lock_staging(root: Path, node: Path, staging: Path, name: str, package: str, version: str, run) -> None:
    """Write *staging*'s package.json and package-lock.json: the committed lock when it covers *version*, else one
    resolved from the registry's metadata with scripts off (``--package-lock-only`` downloads no package code)."""
    lock = committed_lock(name, package, version)
    if lock is not None:
        for file in ("package.json", "package-lock.json"):
            shutil.copyfile(lock / file, staging / file)
        return
    (staging / "package.json").write_text(json.dumps(
        {"name": f"lina-{name}", "private": True, "dependencies": {package: version}}, indent=2) + "\n",
        encoding="utf-8")
    npm(root, node, "install", "--package-lock-only", "--ignore-scripts", cwd=staging,
        label=f"npm lock {package}@{version}", run=run)


def npm_install(root: Path, node: Path, vendor: Path, package: str, version: str, integrity: str,
                run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> Path:
    """*package* @ *version* into ``<vendor>/<version>`` with the pinned Node's npm.

    No package code runs before it is verified: the lock (committed under ``pm/npm-locks``, else resolved with
    scripts off) must record *integrity* for *package*; ``npm ci --ignore-scripts`` then installs exactly that tree
    and refuses any tarball whose bytes do not match its locked integrity; only then does ``npm rebuild`` run the
    install scripts (OCX's ``bun`` fetches its binary, omo prepares its engine). A mismatch keeps nothing."""
    dest = vendor / version
    entry = _locked(dest, package)
    if entry.get("version") == version and entry.get("integrity") == integrity:
        return dest
    staging = vendor / f".{version}.partial"
    shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True)
    try:
        _lock_staging(root, node, staging, vendor.name, package, version, run)
        entry = _locked(staging, package)
        if (entry.get("version"), entry.get("integrity")) != (version, integrity):
            raise OcxError(f"{package}@{version}: the lock records {entry.get('version')} "
                           f"{entry.get('integrity')}, the pin is {integrity}; nothing kept")
        npm(root, node, "ci", "--ignore-scripts", cwd=staging, label=f"npm ci {package}@{version}", run=run)
        npm(root, node, "rebuild", cwd=staging, label=f"npm rebuild {package}@{version}", run=run)
        shutil.rmtree(dest, ignore_errors=True)
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return dest


def install_ocx(root: Path, pin: Pins, node: Path, version: str, integrity: str,
                run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> Path:
    """OCX *version* into ``vendor/ocx/<version>`` (install scripts run: OCX's ``bun`` dependency fetches its
    binary)."""
    return npm_install(root, node, vendor_dir(root), pin.package, version, integrity, run)


def install(root: Path | None = None, *, fetch: Callable[[str, Path], None] = download, out=print) -> int:
    """``lina models install``: the pinned Node and OCX, ``current`` set when there is none yet."""
    root = root or lina_root()
    pin = pins()
    node = install_node(root, pin, fetch)
    out(f"✓ Node {pin.node_version}: {node}")
    dest = install_ocx(root, pin, node, pin.version, pin.integrity)
    out(f"✓ OCX {pin.version}: {dest} ({pin.integrity[:20]}…)")
    data = data_dir(root)
    data.mkdir(mode=0o700, parents=True, exist_ok=True)
    active = current_version(root)
    if active is None:
        set_current(root, pin.version)
        active = pin.version
    if active != pin.version:
        out(f"  current stays {active}; `lina models upgrade {pin.version}` switches to the pin")
    out(f"  data: {data} · port {configured_port(root)} · start it with `lina services start ocx`")
    return 0


def install_step(out=print) -> int:
    """``lina install``'s OCX step."""
    try:
        return install(out=out)
    except OcxError as exc:
        out(f"OCX: {exc}")
        return 1


def configured_port(root: Path | None = None) -> int:
    """``ocx.port`` of the LINA root through the config loader (``DEFAULT_CONFIG`` registers 10100): one OCX serves
    every profile, so the root is bound explicitly whatever profile this process runs as."""
    from hermes_cli.config import load_config_readonly
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override
    token = set_hermes_home_override(root or lina_root())
    try:
        return int(load_config_readonly()["ocx"]["port"])
    finally:
        reset_hermes_home_override(token)


@contextlib.contextmanager
def recovering(recover: Callable[[], object]):
    """For the steps after a serving proxy was stopped: when the block raises, *recover* brings a proxy back before
    the exception goes on, so no failure leaves nothing serving."""
    try:
        yield
    except BaseException:
        recover()
        raise


def write_start_port(data: Path, port: int) -> None:
    """Merge ``"port": port`` into ``<data>/config.json`` before OCX is started on *port*.

    OCX 2.95.0 checks who owns a start by probing its config's port (else 10100), not ``--port``: with an empty data
    dir it finds another OCX on 10100 and refuses ("Proxy ownership is unverified"). OCX writes this file too, so
    every other key is kept."""
    data.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = data / "config.json"
    try:
        config = json.loads(path.read_text(encoding="utf-8-sig")) if path.exists() else {}
    except ValueError as exc:
        raise OcxError(f"{path} is not JSON ({exc}); not starting OCX on it") from exc
    if not isinstance(config, dict):
        raise OcxError(f"{path} is not a JSON object; not starting OCX on it")
    if config.get("port") == port:
        return
    config["port"] = port
    tmp = path.with_name(".config.json.lina-tmp")
    with os.fdopen(os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "wb") as fh:  # windows-footgun: ok — binary
        fh.write((json.dumps(config, indent=2) + "\n").encode("utf-8"))
    os.replace(tmp, path)


def entry_paths(root: Path, version: str) -> tuple[Path, Path]:
    """(bun, OCX's CLI entry) of an installed version: the proxy runs as ``bun <entry> start --port <port>``."""
    prefix = version_dir(root, version) / "node_modules"
    return prefix / "bun" / "bin" / "bun.exe", prefix / Path(*pins().package.split("/")) / "src" / "cli" / "index.ts"


def ocx_argv(root: Path, version: str, *args: str) -> list[str]:
    bun, entry = entry_paths(root, version)
    return [str(bun), str(entry), *args]


def ocx_env(root: Path, data: Path | None = None, home: Path | None = None) -> dict[str, str]:
    """What OCX runs with: its data dir, the isolated HOME and the pinned Node first on PATH. The service and its CLI
    (no *home* given) also get omo's agent dir: OCX writes its catalog for omo (the omo client integration) there, not
    under the isolated HOME. An upgrade trial runs on a copy and never gets it."""
    from hermes_cli.lina_omo import agent_dir
    node = node_dir(root, pins().node_version)
    (home or home_dir(root)).mkdir(parents=True, exist_ok=True)
    env = {"OPENCODEX_HOME": str(data or data_dir(root)), "HOME": str(home or home_dir(root)),
           "PATH": os.pathsep.join([str(node / "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"])}
    if home is None:
        env["OMO_CODING_AGENT_DIR"] = str(agent_dir())
    return env


def port_taken(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.settimeout(1)
        if probe.connect_ex(("127.0.0.1", port)) == 0:
            return True
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)  # a just-stopped listener's TIME_WAIT is free
        try:
            probe.bind(("127.0.0.1", port))
        except OSError:
            return True
    return False


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def http_get(port: int, path: str, timeout: float = 3) -> tuple[int, bytes]:
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, b""
    except (urllib.error.URLError, OSError):
        return 0, b""


def own_proxy_running(root: Path, port: int) -> bool:
    """True only when the OCX answering on *port* is the one whose pid this LINA home's data dir records."""
    status, body = http_get(port, "/healthz")
    pid_file = data_dir(root) / "ocx.pid"
    if status != 200 or not pid_file.exists():
        return False
    try:
        health = json.loads(body)
        return health.get("service") == "opencodex" and int(health.get("pid")) == int(pid_file.read_text(encoding="utf-8-sig").strip())
    except (ValueError, TypeError, AttributeError):
        return False
