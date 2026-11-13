"""The node restarts only its local magpie service and holds the slot lock (lina#589)."""

import fcntl
import json
import subprocess
import textwrap

import pytest

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
    call = node("{service: lab-magpie}")
    assert call("service", "proxy", "restart") == {"service": "proxy", "action": "restart",
                                                   "name": "lab-magpie", "state": "running"}
    assert ["systemctl", "--user", "restart", "lab-magpie"] in systemd.calls
    assert call("service", "proxy", "status")["name"] == "lab-magpie"


def test_the_magpie_service_defaults_to_magpie(node, systemd):
    systemd.active = "failed"
    assert node("{}")("service", "proxy", "status") == {
        "service": "proxy", "action": "status", "name": "magpie", "state": "stopped"}


@pytest.mark.parametrize("proxy_yaml", ["{}", None])
def test_no_restart_while_a_run_is_active_on_the_node(node, systemd, proxy_yaml):
    call = node(proxy_yaml)
    node.held[0] = 1
    assert call("service", "proxy", "restart", code=1) == {
        "service": "proxy", "action": "error", "busy": 1,
        "reason": "1 run(s) active on this node: the magpie restart waits until they end"}
    assert not [argv for argv in systemd.calls if "restart" in argv]



@pytest.mark.parametrize("proxy_yaml", ["{}", None])
def test_no_slot_is_admitted_while_the_proxy_restarts(node, systemd, monkeypatch, tmp_path, proxy_yaml):
    """admit() and reserve() take the slot lock; the restart holds it from the busy check to its end."""
    probes = []

    def restart_probe(*_args):
        with (tmp_path / "node" / "worker" / "slots.lock").open("w") as handle:
            try:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                probes.append("held")
            else:
                probes.append("free")
        return {}

    monkeypatch.setattr(proxy_service, "_magpie_restart", lambda unit: (restart_probe() or True, ""))
    node(proxy_yaml)("service", "proxy", "restart")
    assert probes == ["held"]
