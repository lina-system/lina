"""LINA's services, in one list: what ``lina services`` shows and starts, and what #32 / #35 register on (lina#31).

* ``gateway``: the gateway service (launchd ``ai.lina.gateway*``, systemd ``lina-gateway*``, Windows ``LINA_Gateway*``).
  Install, start, stop and status are the existing ``hermes_cli/gateway*.py`` verbs; this module only calls them.
* ``store``: the memory store (``hermes_cli/store.py``). Postgres and the Honcho api and deriver run in the Docker
  compose project ``lina-store`` with ``restart: unless-stopped``, so Docker keeps them up and no launchd/systemd job
  is written for them.
* ``ocx``: LINA's pinned OCX proxy (#32), launchd ``ai.lina.ocx`` / systemd ``lina-ocx``, written through
  ``service_definition`` by ``hermes_cli/lina_ocx_service.py``.
* ``worker``: ``lina worker serve`` (#35), launchd ``ai.lina.worker`` / systemd ``lina-worker``, written through
  ``service_definition`` by ``hermes_cli/worker/service.py``. Its runs reach models through OCX.
* ``web``: the dashboard server of the LINA root (the app at ``/app/``, #168), launchd ``ai.lina.web`` / systemd
  ``lina-web``, written through ``service_definition`` by ``hermes_cli/lina_web_service.py`` (lina#288).

A LINA gateway never starts beside a Hermes Agent gateway of the same profile name: both would poll the same bot
tokens. ``loaded_hermes_agent_gateway`` is the read-only probe ``gateway/run_bootstrap.py`` asks before it binds.
"""

from __future__ import annotations

import argparse
import os
import plistlib
import shlex
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

# Hermes Agent's gateway names (the product LINA was forked from; it may run on the same account).
HERMES_AGENT_LAUNCHD_LABEL = "ai.hermes.gateway"
HERMES_AGENT_SYSTEMD_UNIT = "hermes-gateway"
_SYSTEMD_RUNNING = frozenset({"active", "activating", "reloading", "deactivating"})


@dataclass(frozen=True)
class Service:
    name: str
    launchd: str
    systemd: str
    windows: str
    issue: str = ""  # the issue that adds a reserved service; empty for one that exists

    def label(self, backend: str | None) -> str:
        return {"launchd": self.launchd, "windows": self.windows}.get(backend or "", self.systemd)


SERVICES = (
    Service("gateway", "ai.lina.gateway", "lina-gateway", "LINA_Gateway"),
    Service("store", "lina-store", "lina-store", "lina-store"),
    Service("ocx", "ai.lina.ocx", "lina-ocx", "LINA_OCX"),
    Service("worker", "ai.lina.worker", "lina-worker", "LINA_Worker"),
    Service("web", "ai.lina.web", "lina-web", "LINA_Web"),
)
BY_NAME = {service.name: service for service in SERVICES}


def _gw():
    from hermes_cli import gateway
    return gateway


def backend() -> str | None:
    return _gw()._service_backend()


def label(service: Service, kind: str | None) -> str:
    """The gateway's label carries this profile's suffix (``ai.lina.gateway-<profile>``); the others are host-wide."""
    if service.name != "gateway":
        return service.label(kind)
    gw = _gw()
    if kind == "launchd":
        return gw.get_launchd_label()
    if kind == "windows":
        return gw._gw_windows().get_task_name()
    return gw.get_service_name()


def _store():
    from hermes_cli.store import Store, store_dir
    return Store(store_dir(), out=lambda line: None)


def _gateway_state() -> str:
    gw = _gw()
    if not gw._is_service_installed():
        return "not installed"
    return "running" if gw._is_service_running() else "stopped"


def _store_state() -> str:
    from hermes_cli.store import StoreError, status
    store = _store()
    if not store.env_file.exists():
        return "not installed"
    try:
        return "running" if status(store) == 0 else "stopped"
    except StoreError as exc:
        return f"stopped ({exc})"


def _ocx_state() -> str:
    from hermes_cli import lina_ocx_service
    return lina_ocx_service.state()


def _worker_state() -> str:
    from hermes_cli.worker import service
    return service.state()


def _web_state() -> str:
    from hermes_cli import lina_web_service
    return lina_web_service.state()


_STATE: dict[str, Callable[[], str]] = {"gateway": _gateway_state, "store": _store_state, "ocx": _ocx_state,
                                        "worker": _worker_state, "web": _web_state}


def state(service: Service) -> str:
    if service.issue:
        return f"not installed yet ({service.issue})"
    return _STATE[service.name]()


def list_lines() -> list[str]:
    kind = backend()
    return [f"{service.name:<8} {label(service, kind):<26} {state(service)}" for service in SERVICES]


def _gateway(verb: str) -> None:
    """The ``hermes gateway`` verbs as they are, in the profile's own scope. ``start`` installs the service first
    when there is none. Host scope, external supervisors, parking a served profile and the live-session question all
    stay the gateway command's own business."""
    gw = _gw()
    if verb == "start" and not gw._is_service_installed():
        gw.gateway_command(argparse.Namespace(gateway_command="install", start_now=True, start_on_login=True))
        return
    gw.gateway_command(argparse.Namespace(gateway_command=verb))


def _root_homes(root: Path) -> set[Path]:
    """The homes of one LINA root: the root and every ``profiles/<name>`` under it."""
    profiles = root / "profiles"
    homes = {root, *(p for p in profiles.iterdir() if p.is_dir())} if profiles.is_dir() else {root}
    return {home.resolve() for home in homes}


def gateway_running() -> bool:
    """Any gateway process of this LINA root, whichever profile it serves.

    ``find_gateway_pids(all_profiles=True)`` is host-wide (every ``gateway run`` of this account: another root's,
    a test's, Hermes Agent's beside LINA), so only the PIDs whose live home is this root or one of its profiles
    count; a gateway whose home cannot be read is not this root's."""
    from hermes_cli.lina_ocx import lina_root
    from hermes_cli.update_fleet_scope import partition_gateway_pids_by_scope
    owned, _foreign = partition_gateway_pids_by_scope(_gw().find_gateway_pids(all_profiles=True),
                                                      _root_homes(lina_root()))
    return bool(owned)


def _store_verb(verb: str) -> None:
    store = _store()
    if not store.env_file.exists():
        from hermes_cli.store import StoreError
        raise StoreError("not installed. Run `lina store install`.")
    if verb in ("stop", "restart"):
        store.compose("stop", what="stopping the store")
    if verb in ("start", "restart"):
        store.start_db()
        store.start_writers()
    print(f"✓ store {verb}: compose project {store.project}")


def _run_store(verb: str) -> None:
    from hermes_cli.store import StoreError
    try:
        _store_verb(verb)
    except StoreError as exc:
        print(f"Store: {exc}")
        sys.exit(1)


def _ocx(verb: str) -> None:
    from hermes_cli import lina_ocx_service
    code = lina_ocx_service.VERBS[verb]()
    if code:
        sys.exit(code)


def _worker(verb: str) -> None:
    from hermes_cli.worker import service
    code = service.VERBS[verb]()
    if code:
        sys.exit(code)


def _web(verb: str) -> None:
    from hermes_cli import lina_web_service
    code = lina_web_service.VERBS[verb]()
    if code:
        sys.exit(code)


def worker_running() -> bool:
    from hermes_cli.worker import service
    return service.running()


_VERBS: dict[str, Callable[[str], None]] = {"gateway": _gateway, "store": _run_store, "ocx": _ocx, "worker": _worker,
                                            "web": _web}

# Who runs on whom: a service is not stopped or restarted while a service that uses it still runs (the gateway's and
# the worker's model calls go through OCX).
USED_BY: dict[str, tuple[str, ...]] = {"store": ("gateway",), "ocx": ("gateway", "worker")}
_RUNNING: dict[str, Callable[[], bool]] = {"gateway": gateway_running, "worker": worker_running}


def act(verb: str, name: str) -> int:
    """``start``/``stop``/``restart`` one service with its own command, unchanged.

    The one rule added here: a service is not stopped or restarted while a service that uses it still runs (the
    gateway uses the memory store and OCX, the worker uses OCX), so the person stops the user first, with that
    service's own command and its own checks (the gateway asks before it interrupts live sessions)."""
    service = BY_NAME[name]
    if name == "ocx":
        from hermes_cli.lina_ocx import OcxError, refuse_external
        try:
            refuse_external(f"lina services {verb} ocx")  # lina#232: an OCX run outside LINA is only read
        except OcxError as exc:
            print(f"OCX: {exc}")
            return 1
    if service.issue:
        print(f"{name} is not installed yet: lina{service.issue} adds it.")
        return 1
    if verb in ("stop", "restart"):
        users = [each for each in USED_BY.get(name, ()) if _RUNNING.get(each, lambda: False)()]
        if users:
            print(f"{name} {verb} refused: {', '.join(users)} still runs on it. "
                  f"Stop {' and '.join(users)} first (`lina services stop {users[0]}`).")
            return 1
    _VERBS[name](verb)
    return 0


def install_gateway_step(*, services: bool, out=print) -> int:
    """``lina install``'s gateway step: install and start it only when asked (``--services``)."""
    if not services:
        out("To run the gateway as a service: lina services start gateway")
        return 0
    try:
        return act("start", "gateway")
    except SystemExit as exc:
        return exc.code if isinstance(exc.code, int) else 1


def service_logs(root: Path, name: str) -> tuple[Path, Path]:
    """``<root>/logs/<name>.log`` and ``<name>.error.log``, the folder made: a launchd job's shell appends to them
    and fails to start when the folder is missing."""
    logs = root / "logs"
    logs.mkdir(parents=True, exist_ok=True)
    return logs / f"{name}.log", logs / f"{name}.error.log"


def service_env(env: dict[str, str]) -> dict[str, str]:
    """*env* without what a Hermes card session's shell carries: ``HERMES_KANBAN_*``, ``HERMES_DELEGATED_CHILD_CONTEXT``
    and a ``HERMES_HOME`` of Hermes Agent's (lina#288). A job started from that shell must not run as that card."""
    from hermes_constants import is_hermes_agent_path
    return {key: value for key, value in env.items()
            if not key.startswith("HERMES_KANBAN_") and key != "HERMES_DELEGATED_CHILD_CONTEXT"
            and not (key == "HERMES_HOME" and is_hermes_agent_path(value))}


def service_definition(kind: str, label: str, argv: list[str], env: dict[str, str], *,
                       logs: tuple[Path, Path]) -> str:
    """A launchd plist or a systemd user unit running *argv* as *label*: ``ai.lina.ocx`` (#32), ``ai.lina.worker``
    (#35) and ``ai.lina.web`` (#288).

    On launchd the job's first program is ``/usr/bin/osascript`` (``gateway_launchd.launchd_program_arguments``):
    macOS privacy judges a job's file access by that program, and a job that starts the system Python or an unsigned
    interpreter directly is refused the LINA root on an external disk or stops on a consent dialog nobody sees
    (lina#288). *logs* (stdout, stderr) are appended by that wrapper; systemd keeps the journal.

    ``MaterializeDatalessFiles``: launchd jobs do not download cloud placeholders by default, so opening a not yet
    downloaded file of a synced Desk would fail in the job and every child it starts (lina#37)."""
    env = service_env(env)
    if kind == "launchd":
        from hermes_cli.gateway_launchd import launchd_program_arguments
        program = launchd_program_arguments(list(argv), *logs)
        return plistlib.dumps({"Label": label, "ProgramArguments": program, "EnvironmentVariables": env,
                               "RunAtLoad": True, "KeepAlive": True, "MaterializeDatalessFiles": True}).decode()
    if kind == "systemd":
        lines = ["[Unit]", f"Description=LINA {label}", "", "[Service]"]
        lines += [f'Environment="{key}={value}"' for key, value in env.items()]
        lines += [f"ExecStart={shlex.join(argv)}", "Restart=always", "", "[Install]", "WantedBy=default.target", ""]
        return "\n".join(lines)
    raise ValueError(f"no service definition for {kind!r}")


def hermes_agent_gateway_name(profile: str, base: str) -> str:
    return base if profile == "default" else f"{base}-{profile}"


def _probe(argv: list[str]) -> subprocess.CompletedProcess | None:
    try:
        return subprocess.run(argv, capture_output=True, text=True, encoding="utf-8",
                              errors="replace", timeout=5, check=False)
    except (OSError, subprocess.SubprocessError):  # no launchctl/systemctl, or it hung: nothing is loaded
        return None


def loaded_hermes_agent_gateway(profile: str) -> str | None:
    """Hermes Agent's gateway service for *profile* when it is loaded on this account, else None.

    Read-only: ``launchctl print`` (both launchd domains) on macOS, ``systemctl --user is-active`` on Linux. It never
    stops, unloads or edits that service.

    Test seam: ``LINA_TEST_NO_HERMES_AGENT_GATEWAY=1`` answers None without reading the account. The gateway probes
    in ``tests/gateway/fixtures`` set it for their child gateway, which must not depend on what the developer's own
    account runs (lina#259); nothing else sets it, and the guard itself stays unconditional."""
    if os.environ.get("LINA_TEST_NO_HERMES_AGENT_GATEWAY") == "1":
        return None
    if sys.platform == "darwin":
        name = hermes_agent_gateway_name(profile, HERMES_AGENT_LAUNCHD_LABEL)
        uid = os.getuid()  # windows-footgun: ok — darwin-only branch
        for domain in (f"gui/{uid}", f"user/{uid}"):
            result = _probe(["launchctl", "print", f"{domain}/{name}"])
            if result is not None and result.returncode == 0:
                return f"{name} (launchd {domain})"
        return None
    if sys.platform.startswith("linux"):
        name = hermes_agent_gateway_name(profile, HERMES_AGENT_SYSTEMD_UNIT)
        result = _probe(["systemctl", "--user", "is-active", name])
        if result is not None and (result.stdout or "").strip() in _SYSTEMD_RUNNING:
            return f"{name}.service (systemd user unit)"
    return None
