"""``lina models upgrade`` / ``rollback`` (lina#32).

An upgrade installs the new OCX beside the current one and starts it on a spare loopback port with a copy of the
data dir; only when its ``GET /v1/models`` answers 200 does it stop the service, back up ``$LINA_HOME/ocx`` to
``ocx.backup`` (OCX may migrate its config forward), switch ``vendor/ocx/current`` and start the service again.
Any failure before the switch leaves ``current`` alone; a service that does not come back after it is switched back
with the backup restored. ``rollback`` switches ``current`` to the recorded previous version and restores the backup.
"""

from __future__ import annotations

import shutil
import subprocess
import time
from pathlib import Path

from hermes_cli import lina_ocx, lina_ocx_service

TRIAL_WAIT_S = 90.0
STOP_WAIT_S = 30.0


def _previous_file(root: Path) -> Path:
    return lina_ocx.vendor_dir(root) / "previous"


def trial(root: Path, version: str, out=print) -> bool:
    """Start *version* on a spare port with a copy of the data dir; True when ``GET /v1/models`` answers 200."""
    work = lina_ocx.vendor_dir(root) / f".trial-{version}"
    shutil.rmtree(work, ignore_errors=True)
    data = lina_ocx.data_dir(root)
    proc = None
    try:
        if data.exists():
            shutil.copytree(data, work / "data", symlinks=True, ignore=shutil.ignore_patterns("ocx.pid", "*.sock"))
        port = lina_ocx.free_port()
        lina_ocx.write_start_port(work / "data", port)  # the copy carries the service's port, where LINA's OCX runs
        env = lina_ocx.ocx_env(root, data=work / "data", home=work / "home")
        # Owned: the trial proxy dies with this command even when the command is killed (no stray OCX on a port).
        from hermes_cli.process_identity import spawn_owned
        proc = spawn_owned(lina_ocx.ocx_argv(root, version, "start", "--port", str(port)), purpose="ocx upgrade trial",
                           env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + TRIAL_WAIT_S
        while time.monotonic() < deadline and proc.poll() is None:
            if lina_ocx.http_get(port, "/v1/models")[0] == 200:
                out(f"✓ OCX {version} answered GET /v1/models on trial port {port}")
                return True
            time.sleep(0.5)
        out(f"OCX {version}: GET /v1/models on trial port {port} never answered 200"
            + (f" (exited {proc.returncode})" if proc.poll() is not None else ""))
        return False
    finally:
        if proc is not None and proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        if proc is not None:
            from hermes_cli.process_identity import release_process_group_once_empty
            release_process_group_once_empty(proc.pid, proc=proc)
        shutil.rmtree(work, ignore_errors=True)


def _restore(root: Path) -> None:
    data, backup = lina_ocx.data_dir(root), lina_ocx.backup_dir(root)
    shutil.rmtree(data, ignore_errors=True)
    shutil.copytree(backup, data, symlinks=True)


def _unprepared(root: Path, previous: str, running: bool, out) -> None:
    """The way back from a failed preparation: the data dir was only read, so drop the partial backup and its
    record, point ``current`` at *previous* again and start it when it was running."""
    shutil.rmtree(lina_ocx.backup_dir(root), ignore_errors=True)
    _previous_file(root).unlink(missing_ok=True)
    lina_ocx.set_current(root, previous)
    out(f"upgrade preparation failed; current stays {previous}")
    if running:
        lina_ocx_service.start(root, out=out)


def _stopped(root: Path, port: int) -> bool:
    """Stop the service and wait until nothing answers on its port."""
    lina_ocx_service.stop(root, out=lambda line: None)
    deadline = time.monotonic() + STOP_WAIT_S
    while lina_ocx.port_taken(port):
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.5)
    return True


def _switch(root: Path, previous: str, version: str, out) -> int:
    port = lina_ocx.configured_port(root)
    running = lina_ocx.own_proxy_running(root, port)
    lina_ocx_service.stop(root, out=lambda line: None)
    backup = lina_ocx.backup_dir(root)
    with lina_ocx.recovering(lambda: _unprepared(root, previous, running, out)):
        shutil.rmtree(backup, ignore_errors=True)
        data = lina_ocx.data_dir(root)
        data.mkdir(mode=0o700, parents=True, exist_ok=True)
        shutil.copytree(data, backup, symlinks=True)
        _previous_file(root).write_text(previous + "\n", encoding="utf-8")
        lina_ocx.set_current(root, version)
    out(f"✓ current: {previous} -> {version} (data backed up to {backup})")
    if not running or lina_ocx_service.start(root, out=out) == 0:
        return 0
    out(f"OCX {version} did not come back as a service; switching back to {previous}")
    if not _stopped(root, port):
        out(f"OCX {version} still holds port {port} after stopping it; current and data left as they are, "
            "stop it by hand and run `lina models rollback`")
        return 1
    lina_ocx.set_current(root, previous)
    _restore(root)
    lina_ocx_service.start(root, out=out)
    return 1


def upgrade(root: Path | None = None, version: str | None = None, integrity: str | None = None, out=print) -> int:
    root = root or lina_ocx.lina_root()
    pin = lina_ocx.pins()
    version = version or pin.version
    integrity = integrity or (pin.integrity if version == pin.version else None)
    if integrity is None:
        out(f"OCX {version} is not the pinned version ({pin.version}): pass its npm integrity with --integrity")
        return 1
    previous = lina_ocx.current_version(root)
    if previous is None:
        out("OCX is not installed. Run `lina models install`.")
        return 1
    if previous == version:
        out(f"✓ OCX {version} is already current")
        return 0
    try:
        node = lina_ocx.install_node(root, pin)
        lina_ocx.install_ocx(root, pin, node, version, integrity)
        healthy = trial(root, version, out=out)
    except lina_ocx.OcxError as exc:
        out(f"OCX: {exc}; current stays {previous}")
        return 1
    if not healthy:
        out(f"current stays {previous}")
        return 1
    return _switch(root, previous, version, out)


def rollback(root: Path | None = None, out=print) -> int:
    root = root or lina_ocx.lina_root()
    record, backup = _previous_file(root), lina_ocx.backup_dir(root)
    previous = record.read_text(encoding="utf-8-sig").strip() if record.exists() else ""
    if not previous or not lina_ocx.version_dir(root, previous).exists() or not backup.exists():
        out("Nothing to roll back to: no upgrade recorded a previous version and data backup")
        return 1
    current = lina_ocx.current_version(root)
    running = lina_ocx.own_proxy_running(root, lina_ocx.configured_port(root))
    lina_ocx_service.stop(root, out=lambda line: None)
    lina_ocx.set_current(root, previous)
    _restore(root)
    record.unlink()
    out(f"✓ current: {current} -> {previous}; data restored from {backup}")
    return lina_ocx_service.start(root, out=out) if running else 0
