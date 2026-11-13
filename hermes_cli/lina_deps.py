"""The runtimes LINA installs for itself (lina#32, lina#34): the pinned Node of ``pm/lina-deps.json``, npm packages
installed with it (omo and the npm runners, ``lina_runners``), and the LINA root they live in.

``pm/lina-deps.json`` pins one Node (>= 24, per-platform sha256); everything lands under the LINA root and nothing
is installed globally:

* ``runtime/node-<ver>/``: Node, unpacked only after its tarball matched the pinned sha256.
* ``<vendor>/<ver>/``: an npm package installed by that Node's npm with ``--prefix``; its ``package-lock.json`` must
  name the pinned version and integrity. ``<vendor>/current`` points at the active version.
* ``runtime/npm-home/``: the ``HOME`` npm runs with, so an install never writes into the account's home.
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
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
_ARCH = {"amd64": "x64", "arm64": "arm64"}
_OS = {"darwin": "darwin", "linux": "linux"}


class DepsError(Exception):
    pass


@dataclass(frozen=True)
class Pins:
    node_version: str
    node_dist: str
    node_sha256: dict[str, str]


def pins(path: Path = PIN_FILE) -> Pins:
    node = json.loads(path.read_text(encoding="utf-8-sig"))["node"]
    return Pins(node["version"], node["dist"], dict(node["sha256"]))


def lina_root() -> Path:
    from hermes_constants import get_default_hermes_root
    return get_default_hermes_root()


def node_dir(root: Path, version: str) -> Path:
    return root / "runtime" / f"node-{version}"


def home_dir(root: Path) -> Path:
    return root / "runtime" / "npm-home"


def current_version(root: Path, vendor: Path) -> str | None:
    link = vendor / "current"
    return os.readlink(link) if link.is_symlink() else None


def set_current(root: Path, version: str, vendor: Path) -> None:
    """Point ``<vendor>/current`` at *version* in one rename, so a reader never sees it missing."""
    link = vendor / "current"
    tmp = link.with_name(".current.tmp")
    tmp.unlink(missing_ok=True)
    os.symlink(version, tmp)
    os.replace(tmp, link)


def platform_key() -> str:
    from hermes_platform.host.facts import native_arch
    system, arch = _OS.get(sys.platform), _ARCH.get(native_arch())
    if not system or not arch:
        raise DepsError(f"no pinned Node for {sys.platform}/{native_arch()}")
    return f"{system}-{arch}"


def download(url: str, dest: Path) -> None:
    try:
        with urllib.request.urlopen(url, timeout=120) as response, open(dest, "wb") as fh:
            shutil.copyfileobj(response, fh)
    except (urllib.error.URLError, OSError) as exc:
        raise DepsError(f"download of {url} failed: {exc}") from exc


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
            raise DepsError(f"checksum mismatch for {name}: expected {pin.node_sha256[key]}, got {got}; nothing unpacked")
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
        raise DepsError(f"{label or 'npm ' + ' '.join(args[:2])} failed: {tail[0]}")


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
    install scripts (omo prepares its engine). A mismatch keeps nothing."""
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
            raise DepsError(f"{package}@{version}: the lock records {entry.get('version')} "
                            f"{entry.get('integrity')}, the pin is {integrity}; nothing kept")
        npm(root, node, "ci", "--ignore-scripts", cwd=staging, label=f"npm ci {package}@{version}", run=run)
        npm(root, node, "rebuild", cwd=staging, label=f"npm rebuild {package}@{version}", run=run)
        shutil.rmtree(dest, ignore_errors=True)
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return dest
