"""``lina devices`` per runner (lina#222): every device shows its runners with one reason when not ready, a runner
waiting for a person's sign-in becomes one question naming the nodes and how, and magpie and worker status/restart go
through the service layer. Probes and runner reports are fakes; the worker service runs through #218's real
negotiated transport against a local device with fake ``stop``/``start``.
"""

from __future__ import annotations

import json
import textwrap

import pytest

from hermes_cli import lina_devices, lina_runners
from hermes_cli.worker import device, protocol, service, settings
from hermes_cli.worker.transport import LocalTransport, NegotiatedTransport, Reply

CODEX = {"name": "codex", "type": "command", "pin": "codex", "pinned": "1.0.0", "installed": "1.0.0", "sign_in": "magpie",
         "sign_in_by": "magpie", "ready": True, "why": None}
GROK_OUT = {"name": "grok", "type": "command", "pin": "grok", "pinned": "1.0.70", "installed": "1.0.70", "sign_in": "signed_out",
            "sign_in_by": "grok", "sign_in_why": "no ~/.grok/auth.json", "ready": False, "why": "sign in"}
GROK_IN = {**GROK_OUT, "sign_in": "signed_in", "sign_in_why": None, "ready": True, "why": None}
DSH = {"name": "dsh", "type": "acp", "pin": "dsh", "pinned": "0.2.1-rc.5", "installed": "0.2.1-rc.5",
       "sign_in": "unknown", "sign_in_by": "unknown",
       "sign_in_why": "no read-only sign-in check is known for this runner", "ready": True, "why": None}


@pytest.fixture
def base(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    root.mkdir()
    (root / "config.yaml").write_text(textwrap.dedent("""\
        devices:
          workers:
            build-a: {host: build-a}
            build-b: {host: build-b}
            build-c: {host: build-c}
        worker:
          min_free_gb: 0
          runners:
            codex: {type: command, argv: [codex, exec, "-"]}
            grok: {type: command, argv: [grok, -p]}
        """), encoding="utf-8")
    found = {"build-a": {"ssh": True, "serve": True, "free_gb": 80, "running": 0},
             "build-b": {"ssh": False, "error": "no route"},
             "build-c": {"ssh": True, "serve": True, "free_gb": 80, "running": 0}}
    reports = {"build-a": [CODEX, GROK_OUT], "build-c": [CODEX, GROK_OUT]}
    monkeypatch.setattr(lina_devices, "probe", lambda dev, root, runner: found[dev.name])
    monkeypatch.setattr(lina_devices, "omo_status", lambda dev: {})
    monkeypatch.setattr(lina_devices, "runner_status", lambda dev, root: reports[dev.name])
    return {"root": root, "reports": reports}


def test_list_shows_each_runner_of_each_device_with_one_reason(base):
    lines: list[str] = []
    assert lina_devices.list_devices(base["root"], as_json=True, out=lines.append) == 0
    rows = {row["name"]: row for row in json.loads(lines[0])}

    assert [(item["name"], item["ready"], item["why"]) for item in rows["build-a"]["runners"]] == [
        ("codex", True, None), ("grok", False, "sign in")]
    assert [(item["name"], item["why"]) for item in rows["build-b"]["runners"]] == [
        ("omo", "unreachable"), ("codex", "unreachable"), ("grok", "unreachable")], \
        "a node ssh cannot reach still lists its runners, omo's block included (lina#531)"

    text: list[str] = []
    lina_devices.list_devices(base["root"], out=text.append)
    assert any(line.strip().startswith("grok") and line.endswith("not ready: sign in") for line in text)


def test_a_runner_with_no_known_sign_in_check_is_shown_ready_not_as_a_sign_in(base):
    """lina#452: `unknown` is a state the node cannot check, not a failure a person must fix."""
    base["reports"]["build-a"] = [CODEX, DSH]

    lines: list[str] = []
    assert lina_devices.list_devices(base["root"], as_json=True, out=lines.append) == 0
    row = next(item for item in json.loads(lines[0])[0]["runners"] if item["name"] == "dsh")
    assert (row["sign_in"], row["ready"], row["why"]) == ("unknown", True, None)
    assert lina_devices.sign_in_questions(
        [lina_devices.readiness(dev, settings.root(base["root"])) for dev in settings.devices(base["root"])
         if dev.name == "build-a"]) == [], "a runner the node cannot check waits for no one"

    text: list[str] = []
    lina_devices.list_devices(base["root"], out=text.append)
    line = next(line for line in text if line.strip().startswith("dsh"))
    assert line.endswith("ready") and "not ready" not in line
    assert "sign-in unknown (no read-only sign-in check is known for this runner)" in line


def test_a_node_that_cannot_be_asked_lists_an_acp_runner_by_its_adapter(base, monkeypatch):
    """lina#452: the pin that serves an `acp` runner is its adapter's, also on the hub's fallback rows."""
    (base["root"] / "config.yaml").write_text(textwrap.dedent("""\
        devices:
          workers:
            build-b: {host: build-b}
        worker:
          runners:
            claude: {type: acp, argv: [claude-agent-acp]}
        """), encoding="utf-8")
    adapter = lina_runners.RunnerPin(name="claude-agent-acp", installer="npm",
                                     package="@zed-industries/claude-agent-acp", version="0.23.1", integrity="sha512-x",
                                     bin="claude-agent-acp", sign_in="magpie", check="package")
    cli = lina_runners.RunnerPin(name="claude", installer="npm", package="@anthropic-ai/claude-code", version="2.1.311",
                                 integrity="sha512-y", bin="claude", sign_in="magpie")
    monkeypatch.setattr(lina_runners, "pins", lambda path=None: {"claude": cli, "claude-agent-acp": adapter})

    rows = lina_devices.runner_rows(lina_devices.device("build-b", base["root"]),
                                    settings.root(base["root"]), {"ssh": False})

    assert [(row["name"], row["pin"], row["pinned"], row["why"]) for row in rows] == [
        ("claude", "claude-agent-acp", "0.23.1", "unreachable")]


def test_a_runner_signed_out_on_some_nodes_is_one_question_naming_them(base):
    base["reports"]["build-c"] = [CODEX, GROK_OUT]
    asks = lina_devices.sign_in_questions(
        [lina_devices.readiness(dev, settings.root(base["root"])) for dev in settings.devices(base["root"])])

    assert len(asks) == 1, "one question per runner, not per node"
    assert asks[0]["runner"] == "grok" and [node["node"] for node in asks[0]["nodes"]] == ["build-a", "build-c"]
    assert asks[0]["nodes"][0]["how"] == "run `lina devices sign-in build-a grok` on the hub"

    base["reports"]["build-a"] = base["reports"]["build-c"] = [CODEX, GROK_IN]
    lines: list[str] = []
    assert lina_devices.sign_in(base["root"], as_json=True, out=lines.append) == 0
    assert json.loads(lines[0]) == []


@pytest.fixture
def here(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    root.mkdir()
    (root / "config.yaml").write_text("devices:\n  workers:\n    here: {host: local}\n", encoding="utf-8")
    worker_root = settings.root(root)
    instance = protocol.begin_instance(worker_root)
    from hermes_cli.worker import serve
    monkeypatch.setattr(serve, "heartbeat_age", lambda root: 1.0)
    effects: list[str] = []
    monkeypatch.setattr(service, "stop", lambda *a, **kw: effects.append("stop") or 0)
    monkeypatch.setattr(service, "start", lambda *a, **kw: effects.append("start") or 0)
    return {"root": root, "instance": instance, "effects": effects}


def test_worker_status_and_restart_go_through_the_negotiated_service_verb(here):
    lines: list[str] = []
    assert lina_devices.service("here", "worker", "status", base=here["root"], as_json=True, out=lines.append) == 0
    assert json.loads(lines[-1])["instance_id"] == here["instance"] and here["effects"] == []

    assert lina_devices.service("here", "worker", "restart", base=here["root"], as_json=True, out=lines.append) == 0
    assert json.loads(lines[-1]) == {"service": "worker", "action": "restart", "instance_id": here["instance"]}
    assert here["effects"] == ["stop", "start"]



def _remote(here, monkeypatch, name="build-x"):
    """A device reached over ssh, whose wire is the real node dispatch of this LINA root."""
    (here["root"] / "config.yaml").write_text(
        f"devices:\n  workers:\n    here: {{host: local}}\n    {name}: {{host: {name}}}\n"
        "worker:\n  runners:\n    mine: {type: command, argv: [mine]}\n", encoding="utf-8")
    worker_root = settings.root(here["root"])
    mine = lina_runners.RunnerPin(name="mine", installer="npm", version="1.0.0", bin="mine", sign_in="unknown",
                                  package="mine", integrity="sha512-x")
    monkeypatch.setattr(lina_runners, "pins", lambda path=None: {"mine": mine})
    monkeypatch.setattr(lina_devices, "for_device", lambda dev, root: NegotiatedTransport(LocalTransport(root), dev.name))
    return lina_devices.device(name, here["root"]), worker_root


def test_runner_rows_come_from_the_devices_hello(here, monkeypatch):
    dev, worker_root = _remote(here, monkeypatch)

    rows = lina_devices.runner_status(dev, worker_root)

    assert rows == protocol.hello(worker_root)["capabilities"]["runners"]
    assert {row["name"]: row["pin"] for row in rows} == {"omo": None, "mine": "mine"}
    assert rows[1]["why"] == "prepare", "the node's own report, not the hub's guess"


def test_a_device_whose_hello_predates_runner_rows_shows_update(here, monkeypatch):
    dev, worker_root = _remote(here, monkeypatch)
    legacy = Reply(1, json.dumps({"action": "error", "reason": "unknown verb hello"}).encode(), "")
    old = Reply(0, json.dumps({"protocol": {"min": 1, "max": 1}, "instance_id": None, "serve": True, "version": "x",
                               "capabilities": {"runners": [{"name": "mine", "type": "command"}], "verbs": ["hello"],
                                                "access_modes": []}}).encode(), "")
    for answer in (legacy, old):
        raw = type("Raw", (), {"call": lambda self, verb, stdin=b"", timeout=120: answer})()
        monkeypatch.setattr(lina_devices, "for_device", lambda dev, root: NegotiatedTransport(raw, dev.name))
        rows = lina_devices.runner_rows(dev, worker_root, {"ssh": True})
        assert [(row["name"], row["why"]) for row in rows] == [("mine", "update")], "omo has its own column"



def test_the_service_verb_keeps_the_218_form_for_the_worker(here):
    worker_root = settings.root(here["root"])

    code, out = device.dispatch(worker_root, ["service", "restart", here["instance"]])
    assert code == 0 and json.loads(out) == {"action": "restart", "instance_id": here["instance"]}
    code, out = device.dispatch(worker_root, ["service", "worker", "stop", here["instance"]])
    assert json.loads(out) == {"service": "worker", "action": "stop", "instance_id": here["instance"]}
    assert here["effects"] == ["stop", "start", "stop"]

    code, out = device.dispatch(worker_root, ["service", "worker", "restart", "someone-else"])
    assert json.loads(out)["stale_instance"] is True and here["effects"] == ["stop", "start", "stop"]
    code, out = device.dispatch(worker_root, ["service", "proxy", "stop"])
    assert json.loads(out)["action"] == "error", "magpie takes status and restart only"
