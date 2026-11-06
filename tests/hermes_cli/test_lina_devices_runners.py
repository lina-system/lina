"""``lina devices`` per runner (lina#222): every device shows its runners with one reason when not ready, a runner
waiting for a person's sign-in becomes one question naming the nodes and how, and OCX and worker status/restart go
through the service layer. Probes and runner reports are fakes; the worker service runs through #218's real
negotiated transport against a local device with fake ``stop``/``start``.
"""

from __future__ import annotations

import json
import textwrap

import pytest

from hermes_cli import lina_devices, lina_services
from hermes_cli.worker import protocol, service, settings

CODEX = {"runner": "codex", "name": "codex", "pinned": "1.0.0", "installed": "1.0.0", "sign_in": "ocx",
         "sign_in_by": "ocx", "ready": True, "why": None}
GROK_OUT = {"runner": "grok", "name": "grok", "pinned": "1.0.70", "installed": "1.0.70", "sign_in": "signed_out",
            "sign_in_by": "grok", "sign_in_why": "no ~/.grok/auth.json", "ready": False, "why": "sign in"}
GROK_IN = {**GROK_OUT, "sign_in": "signed_in", "sign_in_why": None, "ready": True, "why": None}


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

    assert [(item["runner"], item["ready"], item["why"]) for item in rows["build-a"]["runners"]] == [
        ("codex", True, None), ("grok", False, "sign in")]
    assert [(item["runner"], item["why"]) for item in rows["build-b"]["runners"]] == [
        ("codex", "unreachable"), ("grok", "unreachable")], "a node ssh cannot reach still lists its runners"

    text: list[str] = []
    lina_devices.list_devices(base["root"], out=text.append)
    assert any(line.strip().startswith("grok") and line.endswith("not ready: sign in") for line in text)


def test_a_runner_signed_out_on_some_nodes_is_one_question_naming_them(base):
    base["reports"]["build-c"] = [CODEX, GROK_OUT]
    asks = lina_devices.sign_in_questions(
        [lina_devices.readiness(dev, settings.root(base["root"])) for dev in settings.devices(base["root"])])

    assert len(asks) == 1, "one question per runner, not per node"
    assert asks[0]["runner"] == "grok" and [node["node"] for node in asks[0]["nodes"]] == ["build-a", "build-c"]
    assert asks[0]["nodes"][0]["how"] == "run `grok login --device-auth` on build-a"

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


def test_ocx_restart_keeps_the_service_layers_refusal_while_the_worker_runs(here, monkeypatch):
    acted: list[str] = []
    monkeypatch.setattr(lina_services, "state", lambda svc: "running (pid 7)")
    monkeypatch.setitem(lina_services._RUNNING, "worker", lambda: True)
    monkeypatch.setitem(lina_services._RUNNING, "gateway", lambda: False)
    monkeypatch.setitem(lina_services._VERBS, "ocx", lambda verb: acted.append(verb))
    monkeypatch.setattr("hermes_cli.lina_ocx.refuse_external", lambda what, root=None: None)
    lines: list[str] = []

    assert lina_devices.service("here", "ocx", "status", base=here["root"], as_json=True, out=lines.append) == 0
    assert json.loads(lines[-1])["state"] == "running (pid 7)"

    assert lina_devices.service("here", "ocx", "restart", base=here["root"], as_json=True, out=lines.append) == 1
    assert "worker still runs on it" in json.loads(lines[-1])["reason"] and acted == []

    monkeypatch.setitem(lina_services._RUNNING, "worker", lambda: False)
    assert lina_devices.service("here", "ocx", "restart", base=here["root"], as_json=True, out=lines.append) == 0
    assert acted == ["restart"]
