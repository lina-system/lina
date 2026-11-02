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


def current_version(root: Path) -> str | None:
    link = vendor_dir(root) / "current"
    return os.readlink(link) if link.is_symlink() else None


def set_current(root: Path, version: str) -> None:
    """Point ``vendor/ocx/current`` at *version* in one rename, so a reader never sees it missing."""
    link = vendor_dir(root) / "current"
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


def install_ocx(root: Path, pin: Pins, node: Path, version: str, integrity: str,
                run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> Path:
    """OCX *version* into ``vendor/ocx/<version>`` with the pinned Node's npm (install scripts run: OCX's ``bun``
    dependency fetches its binary). The lock must record *integrity*, else nothing is kept."""
    dest = version_dir(root, version)
    entry = _locked(dest, pin.package)
    if entry.get("version") == version and entry.get("integrity") == integrity:
        return dest
    staging = vendor_dir(root) / f".{version}.partial"
    shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True)
    (staging / "package.json").write_text('{"name": "lina-ocx", "private": true}\n', encoding="utf-8")
    try:
        npm_cli = node / "lib" / "node_modules" / "npm" / "bin" / "npm-cli.js"
        result = run([str(node / "bin" / "node"), str(npm_cli), "install", "--prefix", str(staging), "--save-exact",
                      f"{pin.package}@{version}"], env=npm_env(root, node), capture_output=True, text=True,
                     encoding="utf-8", errors="replace", timeout=900, check=False)
        if result.returncode != 0:
            tail = (result.stderr or result.stdout or "").strip().splitlines()[-1:] or ["no output"]
            raise OcxError(f"npm install {pin.package}@{version} failed: {tail[0]}")
        entry = _locked(staging, pin.package)
        if (entry.get("version"), entry.get("integrity")) != (version, integrity):
            raise OcxError(f"{pin.package}@{version}: the lock records {entry.get('version')} "
                           f"{entry.get('integrity')}, the pin is {integrity}; nothing kept")
        shutil.rmtree(dest, ignore_errors=True)
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return dest


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
    """``ocx.port`` from the LINA root's ``config.yaml``: one OCX serves every profile."""
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    path = (root or lina_root()) / "config.yaml"
    config = read_config_doc_readonly(path) if config_exists(path) else None
    section = config.get("ocx") if isinstance(config, dict) else None
    port = section.get("port") if isinstance(section, dict) else None
    return int(port) if port else DEFAULT_PORT


def entry_paths(root: Path, version: str) -> tuple[Path, Path]:
    """(bun, OCX's CLI entry) of an installed version: the proxy runs as ``bun <entry> start --port <port>``."""
    prefix = version_dir(root, version) / "node_modules"
    return prefix / "bun" / "bin" / "bun.exe", prefix / Path(*pins().package.split("/")) / "src" / "cli" / "index.ts"


def ocx_argv(root: Path, version: str, *args: str) -> list[str]:
    bun, entry = entry_paths(root, version)
    return [str(bun), str(entry), *args]


def ocx_env(root: Path, data: Path | None = None, home: Path | None = None) -> dict[str, str]:
    """What OCX runs with: its data dir, the isolated HOME and the pinned Node first on PATH."""
    node = node_dir(root, pins().node_version)
    (home or home_dir(root)).mkdir(parents=True, exist_ok=True)
    return {"OPENCODEX_HOME": str(data or data_dir(root)), "HOME": str(home or home_dir(root)),
            "PATH": os.pathsep.join([str(node / "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"])}


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
