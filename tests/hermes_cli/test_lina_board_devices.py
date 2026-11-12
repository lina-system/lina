"""``GET /api/lina/devices`` by role and the owner's device actions (lina#234), served over a scratch LINA root.

Nodes answer through fake negotiated contacts (``hello`` then ``probe``); ``lina omo status``, the person device's
ssh call, the node service layer, the owner-device gate and lina#230's sign-in start are fakes. Every served body
is validated against ``docs/api/board-client.yaml``.
"""

from __future__ import annotations

import inspect
import json
import logging
import sys
import textwrap
import threading
import time
import types
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from hermes_cli import lina_board_devices, lina_devices, lina_ocx
from hermes_cli.lina_omo import omo_pin
from hermes_cli.lina_runners import pins
from hermes_cli.lina_work_settings_gate import GATE
from hermes_cli.worker.transport import Reply
from tests.hermes_cli.test_lina_board_api import _valid

pytestmark = pytest.mark.platforms("posix")

NOW = 1800000000.0
FLEET = {name: pin.version for name, pin in pins().items()}
OMO = omo_pin().version
READY = {"runner": "omo", "ready": True, "serve": True, "running": 0, "cores": 8, "load": 0.5, "free_gb": 100}
OWNER = {"kind": "app", "principal": "owner@example.test", "device": "owner-phone"}
SECRET_URL = "https://auth.example.test/device?user_code=WXYZ-1234"
SECRET_CODE = "WXYZ-1234"
SECRET_TOKEN = "tok_abcdefghijklmnopqrstuvwxyz0123"


def _row(name, *, installed, sign_in, ready, why, pinned=True, kind="command"):
    return {"name": name, "type": kind, "pin": name if pinned else None, "pinned": FLEET.get(name) if pinned else None,
            "installed": installed, "sign_in": sign_in, "sign_in_by": "ocx", "ready": ready, "why": why}


HELLO = {
    "build-a": [
        _row("omo", installed=None, sign_in=None, ready=None, why=None, pinned=False, kind="omo"),
        _row("codex", installed=FLEET["codex"], sign_in="ocx", ready=True, why=None),
        _row("grok", installed=FLEET["grok"], sign_in="signed_out", ready=False, why="sign in"),
        _row("claude", installed="0.0.1", sign_in="ocx", ready=True, why=None),
        _row("dsh", installed=None, sign_in="unknown", ready=False, why="prepare"),
        _row("cursor-agent", installed=FLEET["cursor-agent"], sign_in="api_key", ready=False, why="something new"),
        _row("mine", installed=None, sign_in=None, ready=None, why=None, pinned=False),
    ],
    "store-b": [_row("omo", installed=None, sign_in=None, ready=None, why=None, pinned=False, kind="omo"),
                _row("codex", installed=FLEET["codex"], sign_in="ocx", ready=True, why=None)],
}


class Contact:
    """One node's negotiated contact: ``hello`` on the first call, then the verb."""

    def __init__(self, nodes, name):
        self.nodes, self.name, self.descriptor = nodes, name, None

    def call(self, verb, stdin=b"", timeout=120):
        self.nodes.calls.append((self.name, verb[0]))
        if self.name == "gone-c":
            return Reply(255, b"", "ssh: connect to host gone-c port 22: No route to host")
        if self.name == "old-d":
            self.descriptor = {"action": "error", "reason": "unknown verb hello"}
        else:
            self.descriptor = {"protocol": {"min": 1, "max": 1},
                               "capabilities": {"runners": HELLO[self.name], **self.nodes.caps.get(self.name, {})}}
        return Reply(0, json.dumps(READY).encode())

    def refresh(self, timeout=120):
        return self.call(["hello"], timeout=timeout)


class Nodes:
    def __init__(self, monkeypatch):
        self.calls: list[tuple[str, str]] = []
        self.caps: dict[str, dict] = {}  # a node's hello capabilities beyond its runner rows (ocx, proxy)
        self.omo = {"build-a": {"current": OMO, "daemon": {"running": True}},
                    "store-b": {"current": "0.0.1", "daemon": {"running": True}}}
        self.omo_reads: list[str] = []
        self.queued: list = []
        self.inline = True
        monkeypatch.setattr(lina_board_devices, "for_device", lambda dev, root: Contact(self, dev.name))
        monkeypatch.setattr(lina_devices, "omo_status", self._omo_status)
        monkeypatch.setattr(lina_board_devices, "_spawn", self._spawn)
        lina_board_devices.OMO.reset()
        lina_board_devices.PERSON.reset()

    def _omo_status(self, dev):
        self.omo_reads.append(dev.name)
        return self.omo.get(dev.name, {"error": "ssh: no route"})

    def _spawn(self, work):
        if self.inline:
            work()
        else:
            self.queued.append(work)

    def run_queued(self):
        work, self.queued = self.queued, []
        for item in work:
            item()


class Ssh:
    """The person device's one ssh call: prints ``@@<bin>`` and the first line of its ``--version``."""

    def __init__(self, monkeypatch):
        self.calls: list[tuple[list, dict]] = []
        self.code = 0
        self.versions = {"codex": f"codex-cli {FLEET['codex']}", "omo": OMO, "grok": "grok version 0.0.9"}
        monkeypatch.setattr(lina_board_devices.subprocess, "run", self._run)

    def _run(self, argv, **kwargs):
        assert argv[0] == "ssh", argv
        self.calls.append((argv, kwargs))
        lines = []
        for name, _bin, _fleet in lina_board_devices._person_tools():
            lines.append(f"@@{_bin}")
            if _bin in self.versions:
                lines.append(self.versions[_bin])
        return types.SimpleNamespace(returncode=self.code, stdout="\n".join(lines) + "\n", stderr="")


@pytest.fixture
def root(tmp_path, monkeypatch):
    lina = tmp_path / "lina"
    lina.mkdir()
    (lina / "config.yaml").write_text(textwrap.dedent("""\
        devices:
          workers:
            build-a: {host: build-a, cap: 2}
            store-b: {host: store-b, role: files}
            gone-c: {host: gone-c, role: work}
            old-d: {host: old-d}
            laptop: {host: laptop-ssh, role: person}
        worker:
          min_free_gb: 0
          runners:
            codex: {type: command, argv: [codex, exec, "-"]}
            grok: {type: command, argv: [grok, -p]}
            mine: {type: command, argv: [mytool]}
        """), encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(lina))
    monkeypatch.setenv("HERMES_HOME", str(lina))
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: lina)
    clock = [NOW]
    monkeypatch.setattr(time, "time", lambda: clock[0])
    return types.SimpleNamespace(path=lina, clock=clock)


@pytest.fixture
def nodes(monkeypatch):
    return Nodes(monkeypatch)


@pytest.fixture
def ssh(monkeypatch):
    return Ssh(monkeypatch)


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _devices(client):
    reply = client.get("/api/lina/devices")
    assert reply.status_code == 200
    return _valid("DevicesView", reply.json())


def _cells(machine):
    return {cell["name"]: (cell["fleet"], cell["installed"], cell["signIn"], cell["ready"], cell["why"])
            for cell in machine["runners"]}


def test_nodes_carry_role_and_reachable_work_first_then_down_first(root, nodes, ssh, client):
    view = _devices(client)

    assert [(m["name"], m["role"], m["reachable"], m["state"]) for m in view["machines"]] == [
        ("gone-c", "work", False, "down"), ("build-a", "work", True, "ok"), ("old-d", "work", True, "ok"),
        ("store-b", "files", True, "ok")]
    assert "laptop" not in [m["name"] for m in view["machines"]]
    assert sorted({verb for _, verb in nodes.calls}) == ["probe"], "one contact per node; hello rides the first call"
    assert not [name for name, _ in nodes.calls if name == "laptop"], "a person device is never contacted as a node"


def test_runner_cells_carry_fleet_installed_sign_in_and_one_reason(root, nodes, ssh, client):
    build = next(m for m in _devices(client)["machines"] if m["name"] == "build-a")

    assert _cells(build) == {
        "omo": (OMO, OMO, "ocx", True, None),
        "codex": (FLEET["codex"], FLEET["codex"], "ocx", True, None),
        "grok": (FLEET["grok"], FLEET["grok"], "signed_out", False, "sign_in"),
        "claude": (FLEET["claude"], "0.0.1", "ocx", False, "update"),
        "dsh": (FLEET["dsh"], None, "unknown", False, "prepare"),
        "cursor-agent": (FLEET["cursor-agent"], FLEET["cursor-agent"], "api_key", False, "unknown"),
        "mine": (None, None, None, None, None),
    }
    assert [cell["type"] for cell in build["runners"]] == ["omo", *["command"] * 6]


def test_an_unreachable_node_lists_the_hub_s_runners_unreachable_and_an_old_one_update(root, nodes, ssh, client):
    machines = {m["name"]: m for m in _devices(client)["machines"]}

    offered = {"omo": OMO, "codex": FLEET["codex"], "grok": FLEET["grok"]}
    assert _cells(machines["gone-c"]) == {name: (fleet, None, "ocx" if name == "omo" else None, False, "unreachable")
                                          for name, fleet in offered.items()}
    assert machines["gone-c"]["why"] == "unreachable"
    assert {name: cell[4] for name, cell in _cells(machines["old-d"]).items()} == dict.fromkeys(offered, "update")


class Raw:
    """A node's raw transport under the real ``NegotiatedTransport``: one fixed answer to ``hello``, READY to
    ``probe``."""

    def __init__(self, hello: Reply):
        self.hello = hello

    def call(self, verb, stdin=b"", timeout=120):
        if self.hello.code == 255:
            return self.hello
        return self.hello if verb[0] == "hello" else Reply(0, json.dumps(READY).encode())


@pytest.mark.parametrize("hello, reachable, why", [
    (Reply(0, json.dumps({"protocol": {"min": 99, "max": 99}}).encode()), True, "update"),
    (Reply(1, b"", "lina: worker crashed"), True, "unknown"),
    (Reply(255, b"", "ssh: connect to host build-a port 22: No route to host"), False, "unreachable"),
], ids=["protocol-range", "hello-failed", "ssh-255"])
def test_a_node_refusing_the_protocol_is_update_a_failed_hello_unknown_only_no_ssh_unreachable(
        root, nodes, ssh, client, monkeypatch, hello, reachable, why):
    from hermes_cli.worker.transport import NegotiatedTransport
    monkeypatch.setattr(lina_board_devices, "for_device", lambda dev, _root: NegotiatedTransport(Raw(hello), dev.name))
    build = next(m for m in _devices(client)["machines"] if m["name"] == "build-a")

    assert build["reachable"] is reachable
    assert {cell["name"]: cell["why"] for cell in build["runners"]} == dict.fromkeys(("omo", "codex", "grok"), why)


def test_roles_come_from_the_device_config_as_lina_229_reads_it(root, nodes, ssh, client, monkeypatch):
    config = root.path / "config.yaml"
    config.write_text(config.read_text().replace("    laptop:", "    nas-e: {host: nas-e, kind: file}\n    laptop:"))
    monkeypatch.setitem(HELLO, "nas-e", [{"name": "omo", "type": "omo", "pin": None, "pinned": None, "installed": OMO,
                                          "sign_in": None, "ready": True, "why": None}])
    view = _devices(client)

    roles = {m["name"]: m["role"] for m in view["machines"]}
    assert (roles["store-b"], roles["nas-e"]) == ("files", "files"), "role: files, and a kind: file node's default"
    assert [p["name"] for p in view["person"]] == ["laptop"] and "laptop" not in roles
    nas = next(m for m in view["machines"] if m["name"] == "nas-e")
    assert _cells(nas) == {"omo": (OMO, OMO, "ocx", True, None)}, "a file node's omo is its hello's, not lina omo status"
    assert "nas-e" not in nodes.omo_reads

def test_the_omo_cell_is_served_from_a_background_read_every_ten_minutes(root, nodes, ssh, client):
    nodes.inline = False
    first = next(m for m in _devices(client)["machines"] if m["name"] == "store-b")
    assert _cells(first)["omo"] == (OMO, None, "ocx", False, "unknown"), "no answer yet: unknown, never a guess"

    nodes.run_queued()
    second = next(m for m in _devices(client)["machines"] if m["name"] == "store-b")
    assert _cells(second)["omo"] == (OMO, "0.0.1", "ocx", False, "update")
    assert nodes.queued == [], "a refresh started less than ten minutes ago is not started again"

    nodes.omo["store-b"] = {"error": "ssh: timed out"}
    root.clock[0] += lina_board_devices.OMO_EVERY_S
    _devices(client)
    nodes.run_queued()
    kept = next(m for m in _devices(client)["machines"] if m["name"] == "store-b")
    assert _cells(kept)["omo"] == (OMO, "0.0.1", "ocx", False, "update"), "a failed read keeps the last answer"

    nodes.omo["store-b"] = {"current": None, "daemon": {"running": True}}
    root.clock[0] += lina_board_devices.OMO_EVERY_S
    _devices(client)
    nodes.run_queued()
    gone = next(m for m in _devices(client)["machines"] if m["name"] == "store-b")
    assert _cells(gone)["omo"] == (OMO, None, "ocx", False, "prepare")


def test_the_omo_cell_needs_the_daemon_ready_by_the_probe(root, nodes, ssh, client, monkeypatch):
    ready = dict(READY, ready=False)
    monkeypatch.setattr(Contact, "call", lambda self, verb, stdin=b"", timeout=120: (
        setattr(self, "descriptor", {"capabilities": {"runners": HELLO["build-a"]}}) or
        Reply(0, json.dumps(ready).encode())) if self.name == "build-a" else Reply(255, b"", "down"))
    build = next(m for m in _devices(client)["machines"] if m["name"] == "build-a")
    assert _cells(build)["omo"] == (OMO, OMO, "ocx", False, "prepare")


def test_person_devices_show_status_and_tool_versions_from_one_read_only_ssh_call(root, nodes, ssh, client):
    nodes.inline = False
    first = _devices(client)["person"]
    assert [(p["name"], p["role"], p["reachable"], p["checkedAt"]) for p in first] == [("laptop", "person", None, None)]

    nodes.run_queued()
    laptop = _devices(client)["person"][0]
    tools = {tool["name"]: (tool["fleet"], tool["installed"]) for tool in laptop["tools"]}
    assert (laptop["reachable"], laptop["checkedAt"]) == (True, NOW)
    assert tools["codex"] == (FLEET["codex"], FLEET["codex"]) and tools["omo"] == (OMO, OMO)
    assert tools["grok"] == (FLEET["grok"], "0.0.9") and tools["claude"] == (FLEET["claude"], None)
    assert "claude-agent-acp" not in tools, "an adapter checked by its package is never started for --version"
    argv, kwargs = ssh.calls[0]
    assert argv[:5] == ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5"] and argv[-2] == "laptop-ssh"
    assert kwargs["timeout"] == lina_board_devices.PERSON_TIMEOUT_S and "-lc" in argv[-1]

    ssh.code = 255
    root.clock[0] += lina_board_devices.PERSON_EVERY_S
    _devices(client)
    nodes.run_queued()
    down = _devices(client)["person"][0]
    assert down["reachable"] is False and down["tools"] == laptop["tools"], "a failed read keeps the last versions"
    assert len(ssh.calls) == 2


def test_the_devices_etag_changes_when_a_background_read_lands(root, nodes, ssh, client):
    nodes.inline = False
    first = client.get("/api/lina/devices")
    nodes.run_queued()
    again = client.get("/api/lina/devices", headers={"If-None-Match": first.headers["etag"]})
    assert again.status_code == 200 and again.headers["etag"] != first.headers["etag"]
    same = client.get("/api/lina/devices", headers={"If-None-Match": again.headers["etag"]})
    assert same.status_code == 304


class Actions:
    def __init__(self, root, monkeypatch):
        self.root = root
        self.gate: tuple = (OWNER, 0, "")
        self.services: list[tuple] = []
        self.service_reply = {"service": "ocx", "action": "restart", "state": "running (pid 4242)"}
        monkeypatch.setattr(GATE, "check", lambda peer, base=None, now=None: self.gate)
        monkeypatch.setattr(lina_devices, "service_call", self._service)

    def _service(self, dev, worker_root, which, action):
        self.services.append((dev.name, which, action))
        return self.service_reply

    def log(self) -> list[dict]:
        path = self.root.path / "board" / "device-actions.jsonl"
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()] if path.exists() else []


@pytest.fixture
def actions(root, nodes, monkeypatch):
    return Actions(root, monkeypatch)


def _action(reply, status):
    assert reply.status_code == status, reply.text
    return _valid("DeviceAction", reply.json()) if status < 300 else reply.json()


def test_restart_goes_through_the_service_layer_and_is_logged(actions, client):
    body = _action(client.post("/api/lina/devices/build-a/restart", json={"service": "ocx"}), 200)

    assert body == {"ok": True, "device": "build-a", "action": "restart", "state": "restarted", "reason": None}
    assert actions.services == [("build-a", "ocx", "restart")]
    (line,) = actions.log()
    assert {k: line[k] for k in ("by", "device", "action", "service", "state", "reason")} == {
        "by": {"principal": OWNER["principal"], "device": OWNER["device"]}, "device": "build-a", "action": "restart",
        "service": "ocx", "state": "restarted", "reason": None}
    assert line["at"] == NOW


@pytest.mark.parametrize("path, body", [("restart", {"service": "worker"}), ("sign-in", {"runner": "grok"})])
def test_both_actions_are_refused_off_the_owner_s_devices(actions, client, path, body):
    actions.gate = (None, 403, "settings are written only from the owner's devices (see worker.settings.owner)")
    reply = client.post(f"/api/lina/devices/build-a/{path}", json=body)
    assert (reply.status_code, reply.json()["detail"]) == (403, actions.gate[2])
    assert actions.services == [] and actions.log() == []


@pytest.mark.parametrize("path, body", [("restart", {"service": "worker"}), ("sign-in", {"runner": "grok"})])
@pytest.mark.parametrize("name, status, detail", [("nowhere", 404, "no device nowhere in devices.workers"),
                                                  ("laptop", 409, "a person device is never a node")])
def test_a_missing_device_is_404_and_a_person_device_409(actions, client, path, body, name, status, detail):
    reply = client.post(f"/api/lina/devices/{name}/{path}", json=body)
    assert (reply.status_code, reply.json()["detail"]) == (status, detail)
    assert actions.services == []
    assert [(line["device"], line["state"], line["reason"]) for line in actions.log()] == [(name, "refused", detail)]


@pytest.mark.parametrize("path, body", [("restart", {"service": "database"}), ("restart", None),
                                        ("sign-in", {"runner": "nobody"}), ("sign-in", {"runner": 3})])
def test_an_unknown_service_or_runner_is_400(actions, client, path, body):
    reply = client.post(f"/api/lina/devices/build-a/{path}", json=body)
    assert reply.status_code == 400 and actions.services == []


@pytest.mark.parametrize("service, reply", [
    ("worker", {"service": "worker", "action": "restart", "instance_id": "w-0002"}),
    ("ocx", {"service": "ocx", "action": "restart", "state": "running (pid 4242)"}),
], ids=["worker", "ocx"])
def test_a_restart_the_node_carried_out_is_restarted(actions, client, service, reply):
    actions.service_reply = reply
    body = _action(client.post("/api/lina/devices/build-a/restart", json={"service": service}), 200)
    assert body["state"] == "restarted" and actions.log()[0]["state"] == "restarted"


@pytest.mark.parametrize("service, reply", [
    ("worker", {"service": "worker"}),
    ("worker", {"service": "worker", "action": "stopped", "instance_id": "w-0002"}),
    ("worker", {"service": "worker", "action": "restart"}),
    ("ocx", {"service": "ocx", "action": "restart", "state": None}),
    ("ocx", {"service": "worker", "action": "restart", "instance_id": "w-0002"}),
], ids=["empty-reply", "unknown-action", "worker-no-instance", "ocx-no-state", "other-service"])
def test_a_restart_without_the_node_s_affirmative_answer_is_502(actions, client, service, reply):
    actions.service_reply = reply
    reply = client.post("/api/lina/devices/build-a/restart", json={"service": service})
    assert (reply.status_code, reply.json()["detail"]) == (502, "build-a gave no restart result")
    assert [(line["state"], line["reason"]) for line in actions.log()] == [("refused", "build-a gave no restart result")]


def test_a_node_that_refuses_the_restart_is_502_with_its_reason(actions, client):
    actions.service_reply = {"service": "worker", "action": "error", "reason": "build-a: worker instance changed"}
    reply = client.post("/api/lina/devices/build-a/restart", json={"service": "worker"})
    assert (reply.status_code, reply.json()["detail"]) == (502, "build-a: worker instance changed")
    assert actions.log()[0]["state"] == "refused"


def test_the_core_s_sign_in_start_takes_the_call_the_route_makes():
    from hermes_cli import lina_sign_in
    inspect.signature(lina_sign_in.start).bind("build-a", "grok", base=Path("/lina"))


def test_sign_in_takes_a_runner_only_the_node_offers(actions, nodes, start, client, root):
    nodes.inline = True
    body = _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "claude"}), 202)
    assert body["state"] == "started" and start.calls == [("build-a", "claude", root.path)]
    assert nodes.calls.count(("build-a", "hello")) == 1, "one contact, only because the hub has no such runner"

    refused = client.post("/api/lina/devices/build-a/sign-in", json={"runner": "nobody"})
    assert (refused.status_code, refused.json()["detail"]) == (400, 'the body is {"runner": "<a runner this node offers>"}')
    assert client.post("/api/lina/devices/store-b/sign-in", json={"runner": "claude"}).status_code == 400


def test_sign_in_is_501_until_lina_230_lands(actions, client, monkeypatch):
    monkeypatch.setitem(sys.modules, lina_board_devices.SIGN_IN_MODULE, None)  # a core without lina#230
    reply = client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"})
    assert (reply.status_code, reply.json()["detail"]) == (501, "sign-in is not on this core yet (lina#230)")


@pytest.fixture
def start(monkeypatch):
    """lina#230's start function as a module; it answers with an address, a code and a token the core must drop."""
    calls: list[tuple] = []
    answer: dict = {"state": "signed_in", "reason": f"open {SECRET_URL} and enter {SECRET_CODE}",
                    "url": SECRET_URL, "code": SECRET_CODE, "token": SECRET_TOKEN}
    module = types.ModuleType(lina_board_devices.SIGN_IN_MODULE)

    def start_fn(node, runner, *, base=None):
        calls.append((node, runner, base))
        if isinstance(answer.get("raise"), Exception):
            raise answer["raise"]
        return dict(answer)
    module.start = start_fn
    monkeypatch.setitem(sys.modules, lina_board_devices.SIGN_IN_MODULE, module)
    return types.SimpleNamespace(calls=calls, answer=answer)


def test_sign_in_starts_in_the_background_answers_202_and_logs_its_end(actions, nodes, start, client, root):
    nodes.inline = False
    body = _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"}), 202)
    assert body == {"ok": True, "device": "build-a", "action": "sign-in", "state": "started", "reason": None}
    assert start.calls == [], "the route never waits for the start"

    nodes.run_queued()
    assert start.calls == [("build-a", "grok", root.path)]
    assert [(line["action"], line["runner"], line["state"]) for line in actions.log()] == [
        ("sign-in", "grok", "started"), ("sign-in", "grok", "signed_in")]


def test_a_sign_in_still_running_is_409_until_it_ends(actions, nodes, start, client):
    nodes.inline = False
    _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"}), 202)
    busy = client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"})
    assert (busy.status_code, busy.json()["detail"]) == (409, "a sign-in to grok on build-a is already running")
    _action(client.post("/api/lina/devices/store-b/sign-in", json={"runner": "grok"}), 202)

    nodes.run_queued()
    _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"}), 202)


def test_no_sign_in_address_code_or_token_reaches_a_reply_the_log_or_the_process_log(actions, nodes, start, client,
                                                                                     root, caplog):
    caplog.set_level(logging.DEBUG)
    replies = [client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"}).text]
    start.answer["state"] = "refused because https://x.test"
    start.answer["raise"] = RuntimeError(f"device flow failed at {SECRET_URL} code {SECRET_CODE}")
    replies.append(client.post("/api/lina/devices/store-b/sign-in", json={"runner": "grok"}).text)

    log = (root.path / "board" / "device-actions.jsonl").read_text(encoding="utf-8")
    assert [line["state"] for line in actions.log()] == ["started", "signed_in", "started", "error"]
    assert actions.log()[1]["reason"] == "open [hidden] and enter [hidden]"
    for text in (*replies, log, caplog.text):
        for secret in (SECRET_URL, SECRET_CODE, SECRET_TOKEN, "auth.example.test", "https://"):
            assert secret not in text


def test_a_runner_cell_says_signing_in_while_its_sign_in_runs_and_not_after(actions, nodes, start, client):
    entered, release = threading.Event(), threading.Event()
    module = sys.modules[lina_board_devices.SIGN_IN_MODULE]
    real_start = module.start

    def waiting_start(node, runner, *, base=None):
        entered.set()
        assert release.wait(10)
        return real_start(node, runner, base=base)
    module.start = waiting_start
    nodes.inline = False

    def signing(reply):
        assert reply.status_code == 200
        view = _valid("DevicesView", reply.json())
        return {(m["name"], cell["name"]) for m in view["machines"] for cell in m["runners"] if cell["signingIn"]}

    _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"}), 202)
    thread = threading.Thread(target=nodes.queued.pop())
    thread.start()
    assert entered.wait(10)
    during = client.get("/api/lina/devices")
    assert signing(during) == {("build-a", "grok")}

    release.set()
    thread.join(10)
    assert not thread.is_alive()
    after = client.get("/api/lina/devices")
    assert signing(after) == set()
    assert after.headers["etag"] != during.headers["etag"]


# lina#589: the node's model proxy by kind, its restart and where its accounts are signed in.
OCX_CAPS = {"ocx": {"external": False, "version": "1.0.0", "health": 200},
            "proxy": {"kind": "ocx", "health": 200, "version": "1.0.0"}}
MAGPIE_CAPS = {"ocx": {"external": False, "version": None, "health": None},
               "proxy": {"kind": "magpie", "health": 200, "models": 23, "remote_id": "hub",
                         "service": {"name": "magpie", "state": "running"}}}


def _machine(client, name):
    return next(m for m in _devices(client)["machines"] if m["name"] == name)


def test_an_ocx_node_shows_its_ocx_and_its_runners_as_before(root, nodes, ssh, client):
    nodes.caps["build-a"] = OCX_CAPS
    build = _machine(client, "build-a")

    assert build["proxy"] == {"kind": "ocx", "health": 200, "version": "1.0.0", "models": None, "link": None,
                              "service": None, "signIn": "node"}
    signs = {name: cell[2] for name, cell in _cells(build).items()}
    assert signs == {"omo": "ocx", "codex": "ocx", "claude": "ocx", "grok": "signed_out", "dsh": "unknown",
                     "cursor-agent": "api_key", "mine": None}


def test_a_magpie_node_shows_its_link_to_the_hub_model_count_and_service_and_no_node_sign_in(root, nodes, ssh,
                                                                                               client):
    nodes.caps["build-a"] = MAGPIE_CAPS
    build = _machine(client, "build-a")

    assert build["proxy"] == {"kind": "magpie", "health": 200, "version": None, "models": 23, "link": "hub",
                              "service": {"name": "magpie", "state": "running"}, "signIn": "hub"}
    signs = {name: cell[2] for name, cell in _cells(build).items()}
    assert signs == {"omo": "magpie", "codex": "magpie", "claude": "magpie", "grok": "signed_out", "dsh": "unknown",
                     "cursor-agent": "api_key", "mine": None}


def test_a_node_reporting_no_proxy_shows_none_and_an_older_node_its_ocx(root, nodes, ssh, client):
    nodes.caps["store-b"] = {"ocx": {"external": False, "version": None, "health": None}}
    nodes.caps["build-a"] = {"ocx": {"external": True, "health": 503}}  # an older node: no capabilities.proxy
    machines = {m["name"]: m for m in _devices(client)["machines"]}

    assert machines["store-b"]["proxy"] is None and machines["old-d"]["proxy"] is None
    assert machines["gone-c"]["proxy"] is None, "an unreachable node shows no proxy, not a guessed one"
    assert machines["build-a"]["proxy"] == {"kind": "ocx", "health": 503, "version": None, "models": None,
                                            "link": None, "service": None, "signIn": "node"}


def test_a_proxy_restart_goes_to_the_node_s_own_proxy_service_and_is_logged(actions, client):
    actions.service_reply = {"service": "proxy", "kind": "magpie", "action": "restart", "name": "magpie",
                             "state": "running"}
    body = _action(client.post("/api/lina/devices/build-a/restart", json={"service": "proxy"}), 200)

    assert body["state"] == "restarted" and actions.services == [("build-a", "proxy", "restart")]
    assert [(line["service"], line["state"]) for line in actions.log()] == [("proxy", "restarted")]


def test_a_proxy_restart_while_a_run_is_active_on_the_node_is_409_with_its_reason(actions, client):
    reason = "1 run(s) active on this node: the magpie restart waits until they end"
    actions.service_reply = {"service": "proxy", "kind": "magpie", "action": "error", "busy": 1, "reason": reason}
    reply = client.post("/api/lina/devices/build-a/restart", json={"service": "proxy"})

    assert (reply.status_code, reply.json()["detail"]) == (409, reason)
    assert [(line["state"], line["reason"]) for line in actions.log()] == [("refused", reason)]


def test_sign_in_through_the_proxy_is_refused_on_a_magpie_node_and_other_runners_still_start(actions, nodes, start,
                                                                                             client, root):
    nodes.caps["build-a"] = MAGPIE_CAPS
    refused = client.post("/api/lina/devices/build-a/sign-in", json={"runner": "codex"})
    assert (refused.status_code, refused.json()["detail"]) == (
        409, "build-a reaches codex through magpie: its account is signed in on the hub's magpie, not on the node")
    assert start.calls == [] and actions.log()[0]["state"] == "refused"

    _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "grok"}), 202)
    assert start.calls == [("build-a", "grok", root.path)]

    nodes.caps["build-a"] = OCX_CAPS
    _action(client.post("/api/lina/devices/build-a/sign-in", json={"runner": "codex"}), 202)
