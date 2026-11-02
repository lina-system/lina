"""LINA's services, in one list: what ``lina services`` shows and starts, and what #32 / #35 register on (lina#31).

* ``gateway``: the gateway service (launchd ``ai.lina.gateway*``, systemd ``lina-gateway*``, Windows ``LINA_Gateway*``).
  Install, start, stop and status are the existing ``hermes_cli/gateway*.py`` verbs; this module only calls them.
* ``store``: the memory store (``hermes_cli/store.py``). Postgres and the Honcho api and deriver run in the Docker
  compose project ``lina-store`` with ``restart: unless-stopped``, so Docker keeps them up and no launchd/systemd job
  is written for them.
* ``ocx``, ``worker``: names reserved for the OCX run command (#32) and ``lina worker`` (#35). Nothing is installed
  for them yet; ``service_definition`` is the writer they can use.

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
    Service("ocx", "ai.lina.ocx", "lina-ocx", "LINA_OCX", issue="#32"),
    Service("worker", "ai.lina.worker", "lina-worker", "LINA_Worker", issue="#35"),
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


_STATE: dict[str, Callable[[], str]] = {"gateway": _gateway_state, "store": _store_state}


def state(service: Service) -> str:
    if service.issue:
        return f"not installed yet ({service.issue})"
    return _STATE[service.name]()


def list_lines() -> list[str]:
    kind = backend()
    return [f"{service.name:<8} {label(service, kind):<26} {state(service)}" for service in SERVICES]


def _gateway(verb: str) -> None:
    """The ``hermes gateway`` verbs, unchanged. ``start`` installs the service first when there is none.

    ``stop`` and ``restart`` name the whole host gateway (``--all``): the memory store is one per LINA root and every
    profile the host serves uses it, so "the gateway is down" has to mean the host, not one parked profile. The
    gateway verb itself asks before interrupting live sessions and exits 1 when they are kept."""
    gw = _gw()
    if verb == "start" and not gw._is_service_installed():
        gw.gateway_command(argparse.Namespace(gateway_command="install", start_now=True, start_on_login=True))
        return
    gw.gateway_command(argparse.Namespace(gateway_command=verb, all=verb in ("stop", "restart")))


def gateway_running() -> bool:
    """Any gateway process of this LINA root, whichever profile it serves."""
    return bool(_gw().find_gateway_pids(all_profiles=True))


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


_VERBS: dict[str, Callable[[str], None]] = {"gateway": _gateway, "store": _run_store}

# Startup order: a service may need the ones before it (the gateway uses the memory store). Stopping runs it backwards.
START_ORDER = ("store", "gateway")


def _phase(verb: str, names: tuple[str, ...]) -> int:
    """Run ``verb`` on ``names`` in order and stop at the first failure, saying what was left as it was."""
    for index, each in enumerate(names):
        try:
            _VERBS[each](verb)
        except SystemExit as exc:
            if exc.code in (None, 0):  # the gateway verbs exit 0 on success
                continue
            if index + 1 < len(names):
                print(f"{each} {verb} failed; left {', '.join(names[index + 1:])} as it was.")
            return exc.code if isinstance(exc.code, int) else 1
    return 0


def _plan(verb: str, name: str) -> list[tuple[str, tuple[str, ...]]]:
    """The phases one request runs, from the dependency order.

    * ``start X`` starts X; ``stop X`` stops X. Stopping a service another one still uses is refused in ``act``.
    * ``restart X`` stops what depends on X and X (top down), then starts them again (bottom up), so nothing runs on a
      service while that service is down.
    * ``all`` is every service that exists."""
    existing = tuple(each for each in START_ORDER if not BY_NAME[each].issue)
    if name == "all":
        chosen = existing
    else:
        chosen = existing[existing.index(name):] if verb == "restart" else (name,)
    down, up = tuple(reversed(chosen)), chosen
    return {"start": [("start", up)], "stop": [("stop", down)], "restart": [("stop", down), ("start", up)]}[verb]


def act(verb: str, name: str) -> int:
    """``start``/``stop``/``restart`` one service, or ``all`` of those that exist.

    The gateway uses the memory store, so services come up bottom-up and go down top-down, each phase stops at its
    first failure (a gateway that kept its live sessions keeps its store; a store that did not come up gets no
    gateway), and the store is not stopped alone while any gateway of this root still runs."""
    if name != "all" and BY_NAME[name].issue:
        print(f"{name} is not installed yet: lina{BY_NAME[name].issue} adds it.")
        return 1
    if verb == "stop" and name == "store" and gateway_running():
        print("store stop refused: the gateway still runs on it. Stop both with `lina services stop all`.")
        return 1
    if name != "all" and verb != "restart":
        _VERBS[name](verb)
        return 0
    for phase_verb, names in _plan(verb, name):
        code = _phase(phase_verb, names)
        if code:
            return code
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


def service_definition(kind: str, label: str, argv: list[str], env: dict[str, str]) -> str:
    """A launchd plist or a systemd user unit running *argv* as *label*. Written by nobody yet: the seam for
    ``ai.lina.ocx`` (#32) and ``ai.lina.worker`` (#35)."""
    if kind == "launchd":
        return plistlib.dumps({"Label": label, "ProgramArguments": list(argv), "EnvironmentVariables": dict(env),
                               "RunAtLoad": True, "KeepAlive": True}).decode()
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
    stops, unloads or edits that service."""
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
