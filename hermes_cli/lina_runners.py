"""LINA's pinned runners (lina#222): install, upgrade, rollback and status of every runner a node offers.

``pm/lina-deps.json`` ``runners`` pins each runner with the installer that owns its binary:

* ``npm``: the pinned Node's npm with ``--prefix`` (``lina_ocx.npm_install``, the same path as omo and OCX); the lock
  must carry the pinned npm integrity before any package code runs.
* ``binary`` / ``tarball``: the per-platform URL the runner's own installer script downloads, checked against the
  pinned ``sha256`` or ``sha512`` before anything is kept.

Each version lives in ``vendor/runners/<name>/<version>``; ``current`` points at the active one and ``previous``
records the one before the last upgrade. ``bin/runners/<name>`` is the shim LINA runs it through; it sets the
runner's ``env`` from the pin (``DISABLE_AUTOUPDATER=1`` for Claude Code), so automatic updates stay off where the
runner documents a switch. Nothing is installed globally, and a runner the person installed (``~/.local/bin``, a
global npm) is only read: its path and ``--version`` are shown as ``person_installed``.

Sign-in is checked read-only, one way per runner (owner, 11-06): Claude and Codex (and their ACP adapters) through
OCX, Grok and Antigravity through their own CLI's credential file (existence only, never its content), Cursor with
``CURSOR_API_KEY``. The node only checks and reports; signing in is the person's (#230).
"""

from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import re
import shlex
import shutil
import subprocess
import tarfile
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx

OcxError = lina_ocx.OcxError
PROBE_TIMEOUT_S = 10.0
_VERSION = re.compile(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?")
SIGN_IN_OK = ("signed_in", "api_key", "ocx")
SERVICE_ACTIONS = ("status", "restart")
# downloads.cursor.com answers 403 to urllib's default ``Python-urllib`` agent.
USER_AGENT = "lina-runners"


class RunnersError(OcxError):
    pass


@dataclass(frozen=True)
class RunnerPin:
    name: str
    installer: str
    version: str
    bin: str
    sign_in: str
    package: str = ""
    integrity: str = ""
    entry: str = ""
    strip: int = 0
    check: str = "version"
    env: dict = field(default_factory=dict)
    artifacts: dict = field(default_factory=dict)


def pins(path: Path = lina_ocx.PIN_FILE) -> dict[str, RunnerPin]:
    table = json.loads(path.read_text(encoding="utf-8-sig")).get("runners", {})
    return {name: RunnerPin(name=name, **spec) for name, spec in table.items()}


def pin(name: str) -> RunnerPin:
    table = pins()
    if name not in table:
        raise RunnersError(f"no pinned runner {name!r} (pinned: {', '.join(table)})")
    return table[name]


def vendor_dir(root: Path, name: str) -> Path:
    return root / "vendor" / "runners" / name


def shim_path(root: Path, name: str) -> Path:
    return root / "bin" / "runners" / name


def current_version(root: Path, runner: RunnerPin) -> str | None:
    return lina_ocx.current_version(root, vendor_dir(root, runner.name))


def entry_path(root: Path, runner: RunnerPin, version: str = "current") -> Path:
    base = vendor_dir(root, runner.name) / version
    return base / "node_modules" / ".bin" / runner.bin if runner.installer == "npm" else base / runner.entry


def installed_versions(root: Path, runner: RunnerPin) -> list[str]:
    base = vendor_dir(root, runner.name)
    if not base.is_dir():
        return []
    return sorted(path.name for path in base.iterdir() if path.is_dir() and not path.is_symlink()
                  and not path.name.startswith(".") and entry_path(root, runner, path.name).exists())


def _node(root: Path, runner: RunnerPin) -> Path | None:
    return lina_ocx.node_dir(root, lina_ocx.pins().node_version) if runner.installer == "npm" else None


def run_env(root: Path, runner: RunnerPin) -> dict[str, str]:
    """The environment LINA runs *runner* in: the caller's, the pin's ``env``, and the pinned Node first on PATH."""
    env = {**os.environ, **runner.env}
    node = _node(root, runner)
    if node is not None:
        env["PATH"] = os.pathsep.join([str(node / "bin"), env.get("PATH", "")])
    return env


def write_shim(root: Path, runner: RunnerPin) -> Path:
    shim = shim_path(root, runner.name)
    shim.parent.mkdir(parents=True, exist_ok=True)
    lines = ["#!/bin/sh", f"# LINA's pinned {runner.name} (lina#222), written by `lina runners install`: "
             f"vendor/runners/{runner.name}/current."]
    lines += [f"export {key}={shlex.quote(value)}" for key, value in runner.env.items()]
    node = _node(root, runner)
    if node is not None:
        lines.append(f"PATH={shlex.quote(str(node / 'bin'))}:\"$PATH\"; export PATH")
    lines.append(f"exec {shlex.quote(str(entry_path(root, runner)))} \"$@\"")
    tmp = shim.with_name(f".{runner.name}.tmp")
    tmp.write_text("\n".join(lines) + "\n", encoding="utf-8")
    tmp.chmod(0o755)
    os.replace(tmp, shim)
    return shim


def version_in(text: str) -> str | None:
    found = _VERSION.search(text or "")
    return found.group(0) if found else None


def ask_version(argv: list[str], env: dict[str, str], run: Callable[..., subprocess.CompletedProcess],
                timeout: float = PROBE_TIMEOUT_S) -> str | None:
    """The version ``<argv> --version`` prints, else None (missing, failing or silent within *timeout*)."""
    try:
        done = run([*argv, "--version"], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True,
                   encoding="utf-8", errors="replace", timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return None
    return version_in(done.stdout or done.stderr) if done.returncode == 0 else None


def reported_version(root: Path, runner: RunnerPin, version: str,
                     run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> str | None:
    """The trial: what an installed *version* says it is. ACP adapters (``check: package``) start a server instead of
    answering ``--version``, so their installed ``package.json`` speaks for them (npm ci already checked its bytes)."""
    if runner.check == "package":
        manifest = vendor_dir(root, runner.name) / version / "node_modules" / runner.package / "package.json"
        try:
            return json.loads(manifest.read_text(encoding="utf-8-sig")).get("version")
        except (OSError, ValueError):
            return None
    return ask_version([str(entry_path(root, runner, version))], run_env(root, runner), run, timeout=60)


def download(url: str, dest: Path) -> None:
    request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(request, timeout=300) as response, open(dest, "wb") as fh:
            shutil.copyfileobj(response, fh)
    except (urllib.error.URLError, OSError) as exc:
        raise RunnersError(f"download of {url} failed: {exc}") from exc


def _digest(path: Path, algo: str) -> str:
    digest = hashlib.new(algo)
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _unpack(runner: RunnerPin, blob: Path, dest: Path) -> None:
    if runner.installer == "binary":
        os.replace(blob, dest / runner.entry)
        return
    with tarfile.open(blob) as archive:
        members = []
        for member in archive.getmembers():
            parts = Path(member.name).parts[runner.strip:]
            if parts:
                member.name = str(Path(*parts))
                members.append(member)
        archive.extractall(dest, members=members, filter="data")
    blob.unlink()


def install_artifact(root: Path, runner: RunnerPin, fetch: Callable[[str, Path], None] = download) -> Path:
    """The pinned artifact of this platform into ``vendor/runners/<name>/<version>``; checked before it is kept."""
    vendor = vendor_dir(root, runner.name)
    dest = vendor / runner.version
    if (dest / runner.entry).exists():
        return dest
    key = lina_ocx.platform_key()
    artifact = runner.artifacts.get(key)
    if not artifact:
        raise RunnersError(f"{runner.name} {runner.version}: no pinned artifact for {key}")
    algo = "sha512" if "sha512" in artifact else "sha256"
    staging = vendor / f".{runner.version}.partial"
    shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True)
    try:
        blob = staging / ".download"
        fetch(artifact["url"], blob)
        got = _digest(blob, algo)
        if got != artifact[algo]:
            raise RunnersError(f"{runner.name} {runner.version}: {algo} mismatch for {artifact['url']}: expected "
                               f"{artifact[algo]}, got {got}; nothing kept")
        _unpack(runner, blob, staging)
        if not (staging / runner.entry).is_file():
            raise RunnersError(f"{runner.name} {runner.version}: {runner.entry} is not in {artifact['url']}")
        (staging / runner.entry).chmod(0o755)
        shutil.rmtree(dest, ignore_errors=True)
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return dest


def _install_version(root: Path, runner: RunnerPin, version: str, integrity: str, fetch, run) -> None:
    """*version* beside the others with the installer that owns the binary; it must report *version* (the trial)."""
    if runner.installer == "npm":
        node = lina_ocx.install_node(root, lina_ocx.pins(), fetch)
        lina_ocx.npm_install(root, node, vendor_dir(root, runner.name), runner.package, version, integrity, run)
    else:
        install_artifact(root, runner, fetch)
    got = reported_version(root, runner, version, run)
    if got != version:
        raise RunnersError(f"{runner.name} {version} installed but it reports {got or 'nothing'}")


def _selected(name: str | None) -> list[RunnerPin]:
    return [pin(name)] if name else list(pins().values())


def install(name: str | None = None, root: Path | None = None, *,
            fetch: Callable[[str, Path], None] = download,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """``lina runners install [name]``: the pinned version of one runner, or of every pinned runner."""
    root = root or lina_ocx.lina_root()
    code = 0
    for runner in _selected(name):
        try:
            _install_version(root, runner, runner.version, runner.integrity, fetch, run)
        except OcxError as exc:
            out(f"{runner.name}: {exc}")
            code = 1
            continue
        active = current_version(root, runner)
        if active is None:
            lina_ocx.set_current(root, runner.version, vendor_dir(root, runner.name))
            active = runner.version
        note = "" if active == runner.version else f"; current stays {active} (`lina runners upgrade {runner.name}`)"
        out(f"✓ {runner.name} {runner.version}: {write_shim(root, runner)}{note}")
    return code


def _previous_file(root: Path, runner: RunnerPin) -> Path:
    return vendor_dir(root, runner.name) / "previous"


def _through_shim(root: Path, runner: RunnerPin, run) -> str | None:
    if runner.check == "package":
        return reported_version(root, runner, "current", run)
    return ask_version([str(shim_path(root, runner.name))], dict(os.environ), run, timeout=60)


def _switch(root: Path, runner: RunnerPin, previous: str, version: str, run, out) -> int:
    vendor = vendor_dir(root, runner.name)
    _previous_file(root, runner).write_text(previous + "\n", encoding="utf-8")
    lina_ocx.set_current(root, version, vendor)
    write_shim(root, runner)
    got = _through_shim(root, runner, run)
    if got == version:
        out(f"✓ {runner.name} current: {previous} -> {version} (`lina runners rollback {runner.name}` switches back)")
        return 0
    lina_ocx.set_current(root, previous, vendor)
    _previous_file(root, runner).unlink()
    out(f"{runner.name} {version} answered {got or 'nothing'} through the shim; switched back to {previous}")
    return 1


def upgrade(name: str, root: Path | None = None, version: str | None = None, integrity: str | None = None, *,
            fetch: Callable[[str, Path], None] = download,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """Install *version* beside the current one, trial it, switch ``current``, and switch back when the switched
    shim does not answer with *version*. Moving a pin across nodes is #238; this upgrades one node."""
    root = root or lina_ocx.lina_root()
    runner = pin(name)
    version = version or runner.version
    if version != runner.version and (runner.installer != "npm" or not integrity):
        out(f"{name} {version} is not the pinned version ({runner.version}): "
            + ("pass its npm integrity with --integrity" if runner.installer == "npm"
               else "move the pin in pm/lina-deps.json"))
        return 1
    previous = current_version(root, runner)
    if previous is None:
        out(f"{name} is not installed. Run `lina runners install {name}`.")
        return 1
    if previous == version:
        out(f"✓ {name} {version} is already current")
        return 0
    try:
        _install_version(root, runner, version, integrity or runner.integrity, fetch, run)
    except OcxError as exc:
        out(f"{name}: trial failed: {exc}; current stays {previous}")
        return 1
    return _switch(root, runner, previous, version, run, out)


def rollback(name: str, root: Path | None = None, *, out=print) -> int:
    root = root or lina_ocx.lina_root()
    runner = pin(name)
    record = _previous_file(root, runner)
    previous = record.read_text(encoding="utf-8-sig").strip() if record.exists() else ""
    if not previous or not entry_path(root, runner, previous).exists():
        out(f"Nothing to roll back to: no upgrade recorded a previous {name} version")
        return 1
    current = current_version(root, runner)
    lina_ocx.set_current(root, previous, vendor_dir(root, runner.name))
    write_shim(root, runner)
    record.unlink()
    out(f"✓ {name} current: {current} -> {previous}")
    return 0


def _file_sign_in(path: Path) -> tuple[str, str | None]:
    """Signed in when the CLI's own credential file exists; it is never opened."""
    shown = f"~/{path.relative_to(Path.home())}" if path.is_relative_to(Path.home()) else str(path)
    return ("signed_in", None) if path.exists() else ("signed_out", f"no {shown}")


def _ocx_sign_in() -> tuple[str, str | None]:
    from hermes_cli import lina_services
    state = lina_services.state(lina_services.BY_NAME["ocx"])
    return ("ocx", None) if state.startswith("running") else ("unknown", f"OCX is {state}")


def _cursor_sign_in() -> tuple[str, str | None]:
    return ("api_key", None) if os.environ.get("CURSOR_API_KEY") else ("signed_out", "CURSOR_API_KEY is not set")


SIGN_IN: dict[str, Callable[[], tuple[str, str | None]]] = {
    "ocx": _ocx_sign_in,
    "grok": lambda: _file_sign_in(Path.home() / ".grok" / "auth.json"),
    "agy": lambda: _file_sign_in(Path.home() / ".gemini" / "antigravity-cli" / "antigravity-oauth-token"),
    "cursor": _cursor_sign_in,
    "unknown": lambda: ("unknown", "no read-only sign-in check is known for this runner"),
}
HOW_TO_SIGN_IN = {
    "ocx": "sign in through LINA's OCX on {node} (`lina models status`)",
    "grok": "run `grok login --device-auth` on {node}",
    "agy": "run `agy` on {node} and finish its sign-in",
    "cursor": "put CURSOR_API_KEY into the LINA .env on {node}",
    "unknown": "sign in to {runner} on {node} by hand",
}


def how_to_sign_in(kind: str, runner: str, node: str) -> str:
    return HOW_TO_SIGN_IN.get(kind, HOW_TO_SIGN_IN["unknown"]).format(runner=runner, node=node)


def person_installed(root: Path, runner: RunnerPin, run: Callable[..., subprocess.CompletedProcess],
                     path: str | None = None) -> dict | None:
    """The same CLI the person installed, found on PATH outside LINA's prefix; only read, never changed."""
    lina_bins = {str(root / "bin"), str(root / "bin" / "runners")}
    dirs = [part for part in (path if path is not None else os.environ.get("PATH", "")).split(os.pathsep)
            if part and part not in lina_bins]
    found = shutil.which(runner.bin, path=os.pathsep.join(dirs))
    if not found:
        return None
    asked = None if runner.check == "package" else ask_version([found], dict(os.environ), run)
    return {"path": found, "version": asked}


def _why(runner: RunnerPin, installed: str | None, sign_in: str) -> str | None:
    if installed is None:
        return "prepare"
    if installed != runner.version:
        return "update"
    if sign_in in SIGN_IN_OK:
        return None
    return "prepare" if runner.sign_in == "ocx" else "sign in"


def row(root: Path, runner: RunnerPin, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
        path: str | None = None) -> dict:
    installed = current_version(root, runner)
    sign_in, sign_in_why = SIGN_IN.get(runner.sign_in, SIGN_IN["unknown"])()
    why = _why(runner, installed, sign_in)
    return {"name": runner.name, "pinned": runner.version, "installed": installed,
            "versions": installed_versions(root, runner), "person_installed": person_installed(root, runner, run, path),
            "sign_in": sign_in, "sign_in_by": runner.sign_in, "sign_in_why": sign_in_why,
            "shim": str(shim_path(root, runner.name)) if shim_path(root, runner.name).exists() else None,
            "ready": why is None, "why": why}


def _rows(root: Path, selected: list[tuple[str, RunnerPin]], run) -> list[dict]:
    """Rows in parallel: each ``--version`` it asks is bounded by ``PROBE_TIMEOUT_S``, so the whole is too."""
    if not selected:
        return []
    with ThreadPoolExecutor(max_workers=len(selected)) as pool:
        rows = list(pool.map(lambda item: {**row(root, item[1], run=run), "runner": item[0]}, selected))
    return rows


def status(root: Path | None = None, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> list[dict]:
    """``lina runners status``: every pinned runner on this node (read-only)."""
    root = root or lina_ocx.lina_root()
    return _rows(root, [(name, runner) for name, runner in pins().items()], run)


def offered(worker: dict) -> list[tuple[str, RunnerPin | None]]:
    """The runners this node offers (``worker.runners``), each with the pin that serves it: by name, else by its
    argv's command. omo has its own pin and status (``lina omo status``) and is left out."""
    from hermes_cli.worker import runners
    table = pins()
    by_bin = {runner.bin: runner for runner in table.values()}
    result = []
    for name, spec in runners.specs(worker).items():
        if spec.get("type") == "omo":
            continue
        argv = spec.get("argv") if isinstance(spec.get("argv"), list) else []
        command = Path(str(argv[0])).name if argv else ""
        result.append((name, table.get(name) or by_bin.get(command)))
    return result


def node_report(worker_root: Path, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> list[dict]:
    """What ``hello`` carries under ``capabilities.runners`` (lina#222): one row per offered runner. A runner with no
    pin is reported as not managed by LINA (``pinned`` None, ``ready`` None)."""
    from hermes_cli.worker import settings
    root = worker_root.parent
    pairs = offered(settings.section(root))
    managed = [(name, runner) for name, runner in pairs if runner is not None]
    rows = {item["runner"]: item for item in _rows(root, managed, run)}
    return [rows.get(name) or {"runner": name, "name": None, "pinned": None, "ready": None, "why": None}
            for name, _ in pairs]


def _ocx_act(action: str) -> dict:
    """``lina services restart ocx`` as it is (its refusals included), with its printed lines as the reason."""
    from hermes_cli import lina_services
    printed = io.StringIO()
    with contextlib.redirect_stdout(printed):
        try:
            code = lina_services.act(action, "ocx")
        except SystemExit as exc:
            code = exc.code if isinstance(exc.code, int) else 1
    state = lina_services.state(lina_services.BY_NAME["ocx"])
    if code:
        return {"service": "ocx", "action": "error", "reason": " ".join(printed.getvalue().split()) or
                f"ocx {action} failed", "state": state}
    return {"service": "ocx", "action": action, "state": state}


def node_service(worker_root: Path, service: str, action: str, instance_id: str = "") -> dict:
    """The node side of ``lina devices service`` (lina#222): status and restart of OCX and the worker through the
    service layer (lina#31). The worker restart keeps #218's compare-and-act on *instance_id*; OCX has no instance,
    its state comes from the service layer."""
    from hermes_cli import lina_services
    from hermes_cli.worker import protocol
    if action not in SERVICE_ACTIONS or service not in ("ocx", "worker"):
        return {"service": service, "action": "error", "reason": "service expects ocx|worker and status|restart"}
    if service == "ocx":
        if action == "status":
            return {"service": "ocx", "action": "status", "state": lina_services.state(lina_services.BY_NAME["ocx"])}
        return _ocx_act(action)
    if action == "status":
        hello = protocol.hello(worker_root)
        return {"service": "worker", "action": "status", "state": lina_services.state(lina_services.BY_NAME["worker"]),
                "serve": hello["serve"], "instance_id": hello["instance_id"]}
    return {"service": "worker", **protocol.service_action(worker_root, action, instance_id)}


def render(rows: list[dict]) -> list[str]:
    lines = []
    for item in rows:
        if item.get("pinned") is None:
            lines.append(f"{item['runner']:<17} not pinned by LINA" + (f" · not ready: {item['why']}" if item.get("why")
                                                                       else ""))
            continue
        person = item.get("person_installed") or {}
        sign = (item.get("sign_in") or "-") + (f" ({item['sign_in_why']})" if item.get("sign_in_why") else "")
        lines.append(f"{item.get('runner') or item['name']:<17} pinned {item['pinned']} · installed "
                     f"{item.get('installed') or '-'} · person {person.get('version') or ('-' if not person else '?')} · "
                     f"sign-in {sign} · " + ("ready" if item["ready"] else f"not ready: {item['why']}"))
    return lines
