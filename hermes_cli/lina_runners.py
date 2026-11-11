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

One runner is one block (lina#531), the same for every tool, omo included: the repo describes the built-in tools in
``pm/lina-deps.json`` ``runners.<name>``, and ``worker.runners.<name>`` in the LINA root's config adds or overrides a
tool with ``install`` (the pin fields below), ``sign_in``, ``models`` (where its model list comes from and how it is
refreshed), ``route`` (a route source such as ``ocx-omo``) and ``probe`` (the one real call, ``trial``). A block may
keep its install outside ``vendor/runners`` (``home``, ``shim``) and run an npm package's script on the pinned Node
(``entry``), which is how omo keeps ``vendor/omo`` and ``bin/omo``.

Sign-in is checked read-only, one way per runner (owner, 11-06): Claude and Codex (and their ACP adapters) through
OCX -- LINA's own service, or the operator's external OCX (``ocx.external``) when it answers ``/healthz`` (lina#452)
-- Grok and Antigravity through their own CLI's credential file (existence only, never its content), Cursor with
``CURSOR_API_KEY``. The node only checks and reports; Grok and Antigravity are signed in from the hub's browser by
``lina devices sign-in <node> <runner>`` (``lina_sign_in``, #230). A runner whose sign-in has no read-only check
(``unknown``) is reported as unknown, never as a failure.
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
from concurrent.futures import Future, ThreadPoolExecutor, wait
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx

OcxError = lina_ocx.OcxError
PROBE_TIMEOUT_S = 10.0
HELLO_TIMEOUT_S = 20.0
_VERSION = re.compile(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?")
SIGN_IN_OK = ("signed_in", "api_key", "ocx", "none")
BLOCK_KEYS = ("sign_in", "models", "route", "probe")  # a config block's parts beside ``install`` (lina#531)
SERVICE_ACTIONS = {"ocx": ("status", "restart"), "worker": ("status", "stop", "restart")}
# downloads.cursor.com answers 403 to urllib's default ``Python-urllib`` agent.
USER_AGENT = "lina-runners"

# Complete ACP permission mediation, not merely support for escalation requests.
# Re-observe each new pin before adding its version; unknown versions fail closed.
# 0.2.1-rc.5: lina#451 native-tool writes sent no permission requests.
# 0.2.2-rc.1: ACP bridges explicit escalation only; standing writes bypass it.
ACP_PERMISSION_OBSERVATIONS: dict[str, dict[str, bool]] = {
    "dsh": {"0.2.1-rc.5": False, "0.2.2-rc.1": False},
}


class RunnersError(OcxError):
    pass


@dataclass(frozen=True)
class RunnerPin:
    name: str
    installer: str
    version: str
    bin: str
    sign_in: str | dict
    package: str = ""
    integrity: str = ""
    entry: str = ""
    strip: int = 0
    check: str = "version"
    env: dict = field(default_factory=dict)
    artifacts: dict = field(default_factory=dict)
    follow: dict = field(default_factory=dict)  # where the daily sync reads the newest version (lina#238)
    trial: dict = field(default_factory=dict)  # the probe: the one real request (lina#238, lina#531)
    models: dict = field(default_factory=dict)  # {file, refresh: {ocx_client}}: the model list and its refresh
    route: dict = field(default_factory=dict)  # {source}: the route source that maps models for this runner
    home: str = ""  # the install's folder under the LINA root; "" = vendor/runners/<name>
    shim: str = ""  # the shim under the LINA root; "" = bin/runners/<name>

    @property
    def probe(self) -> dict:
        return self.trial


_FIELDS = frozenset(RunnerPin.__dataclass_fields__) - {"name"}


def _pin(name: str, spec: dict) -> RunnerPin:
    spec = dict(spec)
    if "probe" in spec:
        spec["trial"] = spec.pop("probe")
    return RunnerPin(name=name, **{key: value for key, value in spec.items() if key in _FIELDS})


def config_blocks(root: Path) -> dict[str, dict]:
    """``worker.runners.<name>`` entries that carry an ``install`` block, as pin specs (lina#531): a tool added or
    re-described by settings alone."""
    from hermes_cli.worker import settings
    configured = settings.section(root).get("runners")
    blocks = {}
    for name, item in (configured.items() if isinstance(configured, dict) else ()):
        if isinstance(item, dict) and isinstance(item.get("install"), dict):
            blocks[str(name)] = {**item["install"], **{key: item[key] for key in BLOCK_KEYS if key in item}}
    return blocks


def pins(path: Path = lina_ocx.PIN_FILE, root: Path | None = None, *, moved: bool = True) -> dict[str, RunnerPin]:
    """The repo pins, each config block laid over its name (lina#531), with the daily sync's moves
    (``<root>/runner-pins.json``, lina#238) on top; a move below the pin is ignored, the pin is the floor."""
    root = root or lina_ocx.lina_root()
    table = json.loads(path.read_text(encoding="utf-8-sig")).get("runners", {})
    for name, block in config_blocks(root).items():
        table[name] = {**table.get(name, {}), **block}
    if moved:
        from hermes_cli import lina_runner_sync_state as sync_state
        table = sync_state.merged(table, root)
    return {name: _pin(name, spec) for name, spec in table.items()}


def pin(name: str) -> RunnerPin:
    table = pins()
    if name not in table:
        raise RunnersError(f"no pinned runner {name!r} (pinned: {', '.join(table)})")
    return table[name]


def vendor_dir(root: Path, runner: RunnerPin | str) -> Path:
    if isinstance(runner, str):
        return root / "vendor" / "runners" / runner
    return root / runner.home if runner.home else root / "vendor" / "runners" / runner.name


def shim_path(root: Path, runner: RunnerPin | str) -> Path:
    if isinstance(runner, str):
        return root / "bin" / "runners" / runner
    return root / runner.shim if runner.shim else root / "bin" / "runners" / runner.name


def current_version(root: Path, runner: RunnerPin) -> str | None:
    return lina_ocx.current_version(root, vendor_dir(root, runner))


def entry_path(root: Path, runner: RunnerPin, version: str = "current") -> Path:
    base = vendor_dir(root, runner) / version
    if runner.installer != "npm":
        return base / runner.entry
    if runner.entry:  # a script of the package, run on the pinned Node
        return base / "node_modules" / runner.package / runner.entry
    return base / "node_modules" / ".bin" / runner.bin


def command(root: Path, runner: RunnerPin, version: str = "current") -> list[str]:
    """How LINA starts *version* of *runner*: its entry, or the pinned Node with the package's script."""
    entry = entry_path(root, runner, version)
    node = _node(root, runner)
    return [str(node / "bin" / "node"), str(entry)] if runner.installer == "npm" and runner.entry and node else \
        [str(entry)]


def installed_versions(root: Path, runner: RunnerPin) -> list[str]:
    base = vendor_dir(root, runner)
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
    shim = shim_path(root, runner)
    shim.parent.mkdir(parents=True, exist_ok=True)
    lines = ["#!/bin/sh", f"# LINA's pinned {runner.name} (lina#222, lina#531), written by `lina runners install`: "
             f"{vendor_dir(root, runner).relative_to(root).as_posix()}/current."]
    lines += [f"export {key}={shlex.quote(value)}" for key, value in runner.env.items()]
    node = _node(root, runner)
    if node is not None:
        lines.append(f"PATH={shlex.quote(str(node / 'bin'))}:\"$PATH\"; export PATH")
    lines.append(f"exec {shlex.join(command(root, runner))} \"$@\"")
    tmp = shim.with_name(f".{shim.name}.tmp")
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
        manifest = vendor_dir(root, runner) / version / "node_modules" / runner.package / "package.json"
        try:
            return json.loads(manifest.read_text(encoding="utf-8-sig")).get("version")
        except (OSError, ValueError):
            return None
    return ask_version(command(root, runner, version), run_env(root, runner), run, timeout=60)


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
    vendor = vendor_dir(root, runner)
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
        lina_ocx.npm_install(root, node, vendor_dir(root, runner), runner.package, version, integrity, run)
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
            install_pin(root, runner, fetch=fetch, run=run, out=out)
        except OcxError as exc:
            out(f"{runner.name}: {exc}")
            code = 1
    return code


def install_pin(root: Path, runner: RunnerPin, *, fetch: Callable[[str, Path], None] = download,
                run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> str:
    """The pinned version of one block beside the others, ``current`` when there was none, and its shim; returns the
    active version. Raises ``OcxError`` when the install or its ``--version`` trial fails."""
    _install_version(root, runner, runner.version, runner.integrity, fetch, run)
    active = current_version(root, runner)
    if active is None:
        lina_ocx.set_current(root, runner.version, vendor_dir(root, runner))
        active = runner.version
    note = "" if active == runner.version else f"; current stays {active} (`lina runners upgrade {runner.name}`)"
    out(f"✓ {runner.name} {runner.version}: {write_shim(root, runner)}{note}")
    return active


def _previous_file(root: Path, runner: RunnerPin) -> Path:
    return vendor_dir(root, runner) / "previous"


def _through_shim(root: Path, runner: RunnerPin, run) -> str | None:
    if runner.check == "package":
        return reported_version(root, runner, "current", run)
    return ask_version([str(shim_path(root, runner))], dict(os.environ), run, timeout=60)


def _switch(root: Path, runner: RunnerPin, previous: str, version: str, run, out) -> int:
    vendor = vendor_dir(root, runner)
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
    return upgrade_pin(root, pin(name), version, integrity, fetch=fetch, run=run, out=out)


def upgrade_pin(root: Path, runner: RunnerPin, version: str | None = None, integrity: str | None = None, *,
                fetch: Callable[[str, Path], None] = download,
                run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """``upgrade`` of one block (the same code for every runner, omo included)."""
    name = runner.name
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
    return rollback_pin(root or lina_ocx.lina_root(), pin(name), out=out)


def rollback_pin(root: Path, runner: RunnerPin, *, out=print) -> int:
    name = runner.name
    record = _previous_file(root, runner)
    previous = record.read_text(encoding="utf-8-sig").strip() if record.exists() else ""
    if not previous or not entry_path(root, runner, previous).exists():
        out(f"Nothing to roll back to: no upgrade recorded a previous {name} version")
        return 1
    current = current_version(root, runner)
    lina_ocx.set_current(root, previous, vendor_dir(root, runner))
    write_shim(root, runner)
    record.unlink()
    out(f"✓ {name} current: {current} -> {previous}")
    return 0


def _file_sign_in(path: Path) -> tuple[str, str | None]:
    """Signed in when the CLI's own credential file exists; it is never opened."""
    shown = f"~/{path.relative_to(Path.home())}" if path.is_relative_to(Path.home()) else str(path)
    return ("signed_in", None) if path.exists() else ("signed_out", f"no {shown}")


def _ocx_sign_in(root: Path) -> tuple[str, str | None]:
    """Signed in through the OCX *root* uses (lina#452): LINA's own service, or the operator's external OCX
    (``ocx.external``) when it answers ``/healthz`` -- read the way ``lina_runner_sync_node.ocx_report`` reads it.
    A node with no OCX at all stays ``unknown`` naming that."""
    from hermes_cli import lina_services
    try:
        external = lina_ocx.external(root) is not None
    except lina_ocx.OcxError as exc:
        return ("unknown", str(exc))
    if external:
        try:
            port = lina_ocx.configured_port(root)
        except (lina_ocx.OcxError, KeyError, ValueError, TypeError):
            port = lina_ocx.DEFAULT_PORT
        status, _ = lina_ocx.http_get(port, "/healthz", timeout=1)
        return ("ocx", None) if status == 200 else ("unknown", f"the external OCX is not answering on port {port}")
    state = lina_services.state(lina_services.BY_NAME["ocx"])
    return ("ocx", None) if state.startswith("running") else ("unknown", f"OCX is {state}")


def _env_sign_in(name: str) -> tuple[str, str | None]:
    return ("api_key", None) if os.environ.get(name) else ("signed_out", f"{name} is not set")


def _cursor_sign_in(root: Path) -> tuple[str, str | None]:
    return _env_sign_in("CURSOR_API_KEY")


SIGN_IN: dict[str, Callable[[Path], tuple[str, str | None]]] = {
    "ocx": _ocx_sign_in,
    "none": lambda root: ("none", None),
    "grok": lambda root: _file_sign_in(Path.home() / ".grok" / "auth.json"),
    "agy": lambda root: _file_sign_in(Path.home() / ".gemini" / "antigravity-cli" / "antigravity-oauth-token"),
    "cursor": _cursor_sign_in,
    "unknown": lambda root: ("unknown", "no read-only sign-in check is known for this runner"),
}
HOW_TO_SIGN_IN = {
    "ocx": "sign in through LINA's OCX on {node} (`lina models status`)",
    "grok": "run `lina devices sign-in {node} {runner}` on the hub",
    "agy": "run `lina devices sign-in {node} {runner}` on the hub",
    "cursor": "put CURSOR_API_KEY into the LINA .env on {node}",
    "env": "put the runner's key into the LINA .env on {node}",
    "unknown": "sign in to {runner} on {node} by hand",
}


def how_to_sign_in(kind: str, runner: str, node: str) -> str:
    return HOW_TO_SIGN_IN.get(kind, HOW_TO_SIGN_IN["unknown"]).format(runner=runner, node=node)


def sign_in_kind(runner: RunnerPin) -> str:
    """The block's sign-in as one word: a named check, or ``file`` / ``env`` for a block that names its own."""
    how = runner.sign_in
    return next((key for key in ("file", "env") if key in how), "unknown") if isinstance(how, dict) else str(how)


def read_sign_in(root: Path, runner: RunnerPin) -> tuple[str, str | None]:
    """The block's sign-in, read-only: ``{file: <path>}`` (exists, never opened), ``{env: <NAME>}`` (set), or a
    named check of ``SIGN_IN``."""
    how = runner.sign_in
    if isinstance(how, dict) and isinstance(how.get("file"), str):
        return _file_sign_in(Path(how["file"]).expanduser())
    if isinstance(how, dict) and isinstance(how.get("env"), str):
        return _env_sign_in(how["env"])
    return SIGN_IN.get(sign_in_kind(runner), SIGN_IN["unknown"])(root)


def person_installed(root: Path, runner: RunnerPin, run: Callable[..., subprocess.CompletedProcess],
                     path: str | None = None) -> dict | None:
    """The same CLI the person installed, found on PATH outside LINA's prefix; only read, never changed."""
    lina_bins = {str(root / "bin"), str(root / "bin" / "runners")}
    dirs = [part for part in (path if path is not None else os.environ.get("PATH", "")).split(os.pathsep)
            if part and part not in lina_bins]
    from hermes_platform.resolver import LookupContext, locate_command
    hit = locate_command(runner.bin, LookupContext(path=os.pathsep.join(dirs)))
    found = hit.command[0] if hit.found and hit.command else None
    if not found:
        return None
    asked = None if runner.check == "package" else ask_version([found], dict(os.environ), run)
    return {"path": found, "version": asked}


def active_version(root: Path, runner: RunnerPin,
                   run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> tuple[str | None, str | None]:
    """What the active install is, read from it rather than from the ``current`` link's name: an npm runner's
    installed ``package.json`` with its entry present, another runner's bounded ``--version``. Returns the version,
    or None and why it cannot say."""
    if current_version(root, runner) is None:
        return None, "not installed"
    entry = entry_path(root, runner)
    if not entry.exists():
        return None, f"{entry} is missing"
    if runner.installer != "npm":
        version = ask_version([str(entry)], run_env(root, runner), run)
        return (version, None) if version else (None, f"{entry} --version gave no version")
    manifest = vendor_dir(root, runner) / "current" / "node_modules" / runner.package / "package.json"
    try:
        version = json.loads(manifest.read_text(encoding="utf-8-sig")).get("version")
    except (OSError, ValueError):
        version = None
    return (version, None) if version else (None, f"{manifest} has no version")


def _why(runner: RunnerPin, installed: str | None, sign_in: str) -> str | None:
    """The one reason this runner cannot take work, or None. A sign-in the node cannot check at all (``unknown``:
    no read-only check is known for the runner) is not a failure, so the row stays ready and shows the unknown
    sign-in with its reason instead (lina#452)."""
    if installed is None:
        return "prepare"
    if installed != runner.version:
        return "update"
    if sign_in in SIGN_IN_OK or sign_in_kind(runner) == "unknown":
        return None
    return "prepare" if sign_in_kind(runner) == "ocx" else "sign in"


def row(root: Path, runner: RunnerPin, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
        path: str | None = None) -> dict:
    installed, installed_why = active_version(root, runner, run)
    sign_in, sign_in_why = read_sign_in(root, runner)
    why = _why(runner, installed, sign_in)
    from hermes_cli import lina_runner_sync_state as sync_state
    return {"name": runner.name, "pin": runner.name, "pinned": runner.version, "pin_from": sync_state.pin_from(root, runner.name),
            "current": current_version(root, runner),
            "installed": installed, "installed_why": installed_why, "versions": installed_versions(root, runner), "person_installed": person_installed(root, runner, run, path),
            "sign_in": sign_in, "sign_in_by": sign_in_kind(runner), "sign_in_why": sign_in_why,
            "shim": str(shim_path(root, runner)) if shim_path(root, runner).exists() else None,
            "ready": why is None, "why": why}


def status(root: Path | None = None, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> list[dict]:
    """``lina runners status``: every pinned runner on this node (read-only), probed in parallel."""
    root = root or lina_ocx.lina_root()
    selected = list(pins().values())
    with ThreadPoolExecutor(max_workers=max(1, len(selected))) as pool:
        return list(pool.map(lambda runner: row(root, runner, run=run), selected))


def offered(worker: dict) -> list[tuple[str, str | None, RunnerPin | None]]:
    """The runners this node offers (``worker.runners``) with their type and the pin that serves each. An ``acp``
    runner is pinned by the command in its ``argv`` first and only then by name, so its row describes the adapter it
    actually runs (``claude-agent-acp``) rather than the CLI that shares its name (lina#452). Every other runner is
    pinned by name, else by its argv's command; omo is a block like any other (lina#531)."""
    from hermes_cli.worker import runners
    table = pins()
    by_bin = {runner.bin: runner for runner in table.values()}
    result = []
    for name, spec in runners.specs(worker).items():
        argv = spec.get("argv") if isinstance(spec.get("argv"), list) else []
        command = Path(str(argv[0])).name if argv else ""
        kind = spec.get("type")
        if kind == "acp":
            pin = by_bin.get(command) or table.get(name)
        else:
            pin = table.get(name) or by_bin.get(command)
        result.append((name, kind, pin))
    return result


def _settled(future: Future, runner: RunnerPin, timeout: float) -> dict:
    """A finished row, or the pin with ``why: unknown`` when its probe failed or did not answer in time."""
    if future.done() and future.exception() is None:
        return future.result()
    detail = f"no answer in {timeout:g}s" if not future.done() else f"probe failed: {future.exception()}"
    return {"pin": runner.name, "pinned": runner.version, "installed": None, "sign_in": None,
            "sign_in_by": sign_in_kind(runner), "ready": False, "why": "unknown", "why_detail": detail}


def node_report(worker_root: Path, *, run: Callable[..., subprocess.CompletedProcess] = subprocess.run,
                timeout: float = HELLO_TIMEOUT_S) -> list[dict]:
    """What ``hello`` carries under ``capabilities.runners`` (lina#222): one row per offered runner with its ``name``
    and ``type``, and for a pinned one its versions, sign-in and one reason. Probes run in parallel and the whole is
    bounded by *timeout*, so hello never waits longer; a probe that fails or is late gives ``why: unknown``. A
    runner with no pin is not managed by LINA (``pinned`` None, ``ready`` None)."""
    from hermes_cli.worker import settings
    root = worker_root.parent
    pairs = offered(settings.section(root))
    pool = ThreadPoolExecutor(max_workers=max(1, len(pairs)))
    futures = {name: pool.submit(row, root, pin, run=run) for name, _, pin in pairs if pin is not None}
    wait(list(futures.values()), timeout=timeout)
    pool.shutdown(wait=False, cancel_futures=True)
    rows = []
    for name, kind, pin in pairs:
        found = _settled(futures[name], pin, timeout) if pin is not None else \
            {"pin": None, "pinned": None, "ready": None, "why": None}
        rows.append({**found, "name": name, "type": kind})
    return rows


REPORT_CACHE = "runners.report.json"
REPORT_MAX_AGE_S = 60.0


def clear_report_cache(worker_root: Path) -> None:
    """Drop the cached report so the next ``hello`` probes again (after a sign-in, lina#230)."""
    (worker_root / REPORT_CACHE).unlink(missing_ok=True)


def cached_node_report(worker_root: Path, *, max_age: float = REPORT_MAX_AGE_S) -> list[dict]:
    """``node_report`` for ``hello``: every hub contact asks hello first, so the probes (each CLI's ``--version`` and
    sign-in check) run at most once per *max_age* on this node; a fresh read replaces the file atomically."""
    import time
    path = worker_root / REPORT_CACHE
    try:
        data = json.loads(path.read_text(encoding="utf-8-sig"))
        if isinstance(data, dict) and time.time() - float(data.get("at", 0)) < max_age and \
                isinstance(data.get("rows"), list):
            return data["rows"]
    except (OSError, ValueError, TypeError):
        pass
    rows = node_report(worker_root)
    try:
        worker_root.mkdir(parents=True, exist_ok=True)
        tmp = path.with_name(f".{path.name}.{os.getpid()}")
        tmp.write_text(json.dumps({"at": time.time(), "rows": rows}, ensure_ascii=False), encoding="utf-8")
        os.replace(tmp, path)
    except OSError:
        pass
    return rows


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
    if action not in SERVICE_ACTIONS.get(service, ()):
        return {"service": service, "action": "error",
                "reason": "service expects ocx status|restart or worker status|stop|restart"}
    if service == "ocx":
        if action == "status":
            return {"service": "ocx", "action": "status", "state": lina_services.state(lina_services.BY_NAME["ocx"])}
        return _ocx_act(action)
    if action == "status":
        serve, instance_id = protocol.identity(worker_root)
        return {"service": "worker", "action": "status", "state": lina_services.state(lina_services.BY_NAME["worker"]),
                "serve": serve, "instance_id": instance_id}
    return {"service": "worker", **protocol.service_action(worker_root, action, instance_id)}


def render(rows: list[dict]) -> list[str]:
    lines = []
    for item in rows:
        if item.get("pinned") is None:
            lines.append(f"{item['name']:<17} not pinned by LINA" + (f" · not ready: {item['why']}" if item.get("why")
                                                                       else ""))
            continue
        person = item.get("person_installed") or {}
        sign = (item.get("sign_in") or "-") + (f" ({item['sign_in_why']})" if item.get("sign_in_why") else "")
        lines.append(f"{item['name']:<17} pinned {item['pinned']} · installed "
                     f"{item.get('installed') or '-'} · person {person.get('version') or ('-' if not person else '?')} · "
                     f"sign-in {sign} · " + ("ready" if item["ready"] else f"not ready: {item['why']}"))
    return lines
