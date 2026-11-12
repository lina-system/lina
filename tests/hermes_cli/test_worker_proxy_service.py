"""The node's own model proxy service by its kind (lina#589), through the node's ``service proxy`` verb: ``magpie``
restarts the local service ``worker.proxy.service`` names (default ``magpie``), ``ocx`` the OCX as ``service ocx``
does, and neither restarts while a run holds a slot on the node. systemd and launchd are local fakes.
"""

import json
import subprocess
import textwrap

import pytest

from hermes_cli import lina_runners
from hermes_cli.worker import device, proxy_service, slots

pytestmark = pytest.mark.platforms("posix")


class Systemd:
    def __init__(self, monkeypatch):
        self.calls: list[list[str]] = []
        self.active = "active"
        monkeypatch.setattr(proxy_service, "_backend", lambda: "systemd")
        monkeypatch.setattr(proxy_service, "_run", self._run)

    def _run(self, argv):
        self.calls.append(argv)
        out = f"LoadState=loaded\nActiveState={self.active}\n" if "show" in argv else ""
        return subprocess.CompletedProcess(argv, 0, out, "")


@pytest.fixture
def systemd(monkeypatch):
    return Systemd(monkeypatch)


@pytest.fixture
def node(tmp_path, monkeypatch):
    """*make(proxy_yaml)*: a node root with that ``worker.proxy`` (None: absent); returns ``call(*verb)``."""
    held = [0]
    monkeypatch.setattr(slots, "held", lambda root, *args, **kwargs: held[0])

    def make(proxy_yaml=None):
        base = tmp_path / "node"
        base.mkdir(exist_ok=True)
        text = "devices:\n  workers:\n    me: {host: local}\nworker:\n  default_runner: claude\n"
        if proxy_yaml:
            text += textwrap.indent(f"proxy: {proxy_yaml}\n", "  ")
        (base / "config.yaml").write_text(text, encoding="utf-8")

        def call(*verb, code=0):
            got, out = device.dispatch(base / "worker", list(verb), b"")
            assert got == code, out
            return json.loads(out)
        return call
    make.held = held
    return make


def test_a_magpie_node_restarts_the_local_service_its_setting_names(node, systemd):
    call = node("{kind: magpie, service: lab-magpie}")
    assert call("service", "proxy", "restart") == {"service": "proxy", "kind": "magpie", "action": "restart",
                                                   "name": "lab-magpie", "state": "running"}
    assert ["systemctl", "--user", "restart", "lab-magpie"] in systemd.calls
    assert call("service", "proxy", "status")["name"] == "lab-magpie"


def test_the_magpie_service_defaults_to_magpie(node, systemd):
    systemd.active = "failed"
    assert node("{kind: magpie}")("service", "proxy", "status") == {
        "service": "proxy", "kind": "magpie", "action": "status", "name": "magpie", "state": "stopped"}


@pytest.mark.parametrize("proxy_yaml, kind", [("{kind: magpie}", "magpie"), (None, "ocx")])
def test_no_restart_while_a_run_is_active_on_the_node(node, systemd, monkeypatch, proxy_yaml, kind):
    ocx = []
    monkeypatch.setattr(lina_runners, "_ocx_act", lambda action: ocx.append(action) or {})
    call = node(proxy_yaml)
    node.held[0] = 1
    assert call("service", "proxy", "restart", code=1) == {
        "service": "proxy", "kind": kind, "action": "error", "busy": 1,
        "reason": f"1 run(s) active on this node: the {kind} restart waits until they end"}
    assert not [argv for argv in systemd.calls if "restart" in argv] and ocx == []


def test_an_ocx_node_restarts_its_ocx_as_service_ocx_does(node, systemd, monkeypatch):
    monkeypatch.setattr(lina_runners, "_ocx_act",
                        lambda action: {"service": "ocx", "action": action, "state": "running (1.0.0, port 10100)"})
    assert node()("service", "proxy", "restart") == {"service": "proxy", "kind": "ocx", "action": "restart",
                                                     "state": "running (1.0.0, port 10100)"}
    assert not [argv for argv in systemd.calls if "restart" in argv]
