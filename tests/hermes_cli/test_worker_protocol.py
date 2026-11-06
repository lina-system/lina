"""Regression for #218: negotiate worker capabilities before work or service control.

Wire fakes sit below negotiation; local tests exercise real dispatch and on-disk
identity. No test starts a runner, SSH command, or operating-system service.
"""

import json
import os
import time
from pathlib import Path

import pytest


@pytest.fixture(autouse=True)
def fixed_clock(monkeypatch):
    """Keep freshness assertions independent of scheduler delays and wall time."""
    monkeypatch.setattr(time, "time", lambda: 2_000_000_000.0)


def _descriptor(minimum=1, maximum=1, *, instance="instance-a", version="unrelated-release"):
    return {
        "protocol": {"min": minimum, "max": maximum},
        "instance_id": instance,
        "serve": True,
        "version": version,
        "capabilities": {
            "runners": [{"name": "fake", "type": "command"}],
            "access_modes": [],
            "verbs": ["hello", "probe", "put", "untar", "reserve", "send", "follow_up",
                      "answer", "meta", "human", "pack", "bundle", "cancel", "service"],
        },
    }


def _reply(value, code=0, err=""):
    from hermes_cli.worker.transport import Reply

    return Reply(code, json.dumps(value).encode(), err)


class Wire:
    """Mutable peer: advertise its current descriptor and record accepted payloads."""

    def __init__(self, hello):
        self.hello = hello
        self.received = []

    def call(self, verb, stdin=b"", timeout=120):
        self.received.append((list(verb), stdin))
        if verb == ["hello"]:
            return self.hello
        return _reply({"action": "accepted", "verb": verb[0], "payload": stdin.decode()})


def _refusal(reply):
    value = reply.json()
    assert reply.code != 0
    assert value["action"] == "error"
    assert value["protocol_error"] is True
    assert isinstance(value["reason"], str) and value["reason"]
    assert reply.err == value["reason"]
    return value["reason"]


def test_hello_advertises_the_real_dispatch_registry_and_configured_runners(tmp_path):
    from hermes_cli.worker import device, protocol, runners, settings

    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text(
        "worker:\n  runners:\n    fake:\n      type: command\n      argv: [not-executed]\n",
        encoding="utf-8",
    )
    root = base / "worker"

    code, output = device.dispatch(root, ["hello"])

    assert code == 0
    hello = json.loads(output)
    assert hello == protocol.hello(root)
    assert hello["protocol"] == {"min": protocol.PROTOCOL_MIN, "max": protocol.PROTOCOL_MAX}
    assert type(protocol.PROTOCOL_MIN) is int and type(protocol.PROTOCOL_MAX) is int
    assert 0 <= protocol.PROTOCOL_MIN <= protocol.PROTOCOL_MAX
    assert hello["instance_id"] is None and hello["serve"] is False
    assert isinstance(hello["version"], str) and hello["version"]
    registry = protocol.verbs()
    assert set(hello["capabilities"]["verbs"]) == set(registry)
    assert {entry["name"]: entry["type"] for entry in hello["capabilities"]["runners"]} == {
        name: spec["type"] for name, spec in runners.specs(settings.section(base)).items()
    }
    assert all({"pin", "pinned", "ready", "why"} <= set(entry) for entry in hello["capabilities"]["runners"])
    assert isinstance(hello["capabilities"]["access_modes"], list)
    assert {name for name, verb in registry.items() if verb.legacy} == {
        "meta", "human", "pack", "bundle", "cancel",
    }
    assert all(callable(verb.handler) and isinstance(verb.stdin, bool) for verb in registry.values())
    payload = b"the payload must reach the registered handler\n"
    put_code, _ = device.dispatch(root, ["put", "OT-PROTOCOL", "prompt.1"], payload)
    assert registry["put"].stdin is True
    assert put_code == 0
    assert (root / "tasks" / "OT-PROTOCOL" / "prompt.1").read_bytes() == payload


def test_each_serve_start_publishes_a_new_identity_before_the_first_pass(tmp_path, monkeypatch):
    from hermes_cli.worker import serve, settings

    root = tmp_path / "worker"
    observed = []
    monkeypatch.setattr(settings, "devices", lambda base: [])

    def pass_once(worker_root):
        record = json.loads((worker_root / "serve.instance.json").read_text())
        observed.append(record)
        (worker_root / "serve.heartbeat").write_text(f"{time.time()}\n")

    monkeypatch.setattr(serve, "serve_once", pass_once)

    assert serve.serve(root, once=True) == 0
    assert serve.serve(root, once=True) == 0

    assert len(observed) == 2
    assert observed[0]["instance_id"] != observed[1]["instance_id"]
    for record in observed:
        assert isinstance(record["instance_id"], str) and record["instance_id"]
        assert record["pid"] == os.getpid()
        assert record["started_at"]


def test_begin_instance_returns_the_persisted_identity(tmp_path):
    from hermes_cli.worker import protocol

    root = tmp_path / "worker"

    instance_id = protocol.begin_instance(root)

    record = json.loads((root / "serve.instance.json").read_text())
    assert record["instance_id"] == instance_id
    assert isinstance(instance_id, str) and instance_id
    assert record["pid"] == os.getpid()
    assert record["started_at"]


def test_dispatch_cannot_execute_a_verb_removed_from_the_registry(tmp_path, monkeypatch):
    from hermes_cli.worker import device, protocol

    registry = protocol.verbs()
    registry.pop("put")
    monkeypatch.setattr(protocol, "verbs", lambda: registry)

    code, out = device.dispatch(tmp_path, ["put", "OT-PROTOCOL", "prompt.1"], b"not accepted")

    assert code == 1 and json.loads(out)["action"] == "error"
    assert not (tmp_path / "tasks" / "OT-PROTOCOL" / "prompt.1").exists()


def test_stale_heartbeat_never_advertises_the_persisted_instance(tmp_path):
    from hermes_cli.worker import protocol

    root = tmp_path / "worker"
    protocol.begin_instance(root)
    heartbeat = root / "serve.heartbeat"
    heartbeat.write_text("1\n")
    os.utime(heartbeat, (1, 1))

    hello = protocol.hello(root)

    assert hello["serve"] is False
    assert hello["instance_id"] is None


def test_millisecond_rounded_heartbeat_keeps_the_live_identity(tmp_path):
    from hermes_cli.worker import protocol

    instance = protocol.begin_instance(tmp_path)
    (tmp_path / "serve.heartbeat").write_text(f"{time.time() + 0.0004}\n")

    hello = protocol.hello(tmp_path)

    assert hello["serve"] is True and hello["instance_id"] == instance


def test_overlapping_protocol_accepts_work_despite_a_different_release():
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply(_descriptor(1, 9, version="0.0.0-foreign-release")))
    transport = NegotiatedTransport(raw, "far")

    reply = transport.call(["put", "OT-PROTOCOL", "prompt.1"], b"new work")

    assert reply.code == 0
    assert reply.json()["payload"] == "new work"
    assert raw.received == [(["hello"], b""), (["put", "OT-PROTOCOL", "prompt.1"], b"new work")]


@pytest.mark.parametrize("minimum,maximum", [(0, 0), (2, 3)])
@pytest.mark.parametrize("verb", ["probe", "put", "meta", "human", "pack", "bundle", "cancel", "service"])
def test_disjoint_protocol_denies_even_recovery_verbs(minimum, maximum, verb):
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply(_descriptor(minimum, maximum)))

    reply = NegotiatedTransport(raw, "far").call([verb, "OT-PROTOCOL"])

    reason = _refusal(reply)
    assert f"{minimum}-{maximum}" in reason and "1-1" in reason
    assert reason.endswith("update this hub" if minimum > 1 else "update far")
    assert raw.received == [(["hello"], b"")]


@pytest.mark.parametrize("verb", ["meta", "human", "pack", "bundle", "cancel"])
def test_exact_legacy_unknown_hello_preserves_recovery(verb):
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply({"action": "error", "reason": "unknown verb hello"}, code=1))

    reply = NegotiatedTransport(raw, "old").call([verb, "OT-PROTOCOL"], b"recover")

    assert reply.code == 0 and reply.json()["payload"] == "recover"
    assert raw.received[-1] == ([verb, "OT-PROTOCOL"], b"recover")


@pytest.mark.parametrize("verb", ["probe", "put", "untar", "reserve", "send", "follow_up", "answer", "service"])
def test_legacy_peer_rejects_new_work_and_service_control(verb):
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply({"action": "error", "reason": "unknown verb hello"}, code=1))

    reply = NegotiatedTransport(raw, "old").call([verb, "OT-PROTOCOL"], b"must not arrive")

    _refusal(reply)
    assert raw.received == [(["hello"], b"")]


@pytest.mark.parametrize("answer", [
    b"not JSON", b"[]", b"{}", b'{"protocol":{"min":1}}',
    b'{"protocol":{"min":2,"max":1}}',
    b'{"protocol":{"min":"1","max":1}}',
    b'{"action":"error","reason":"unknown verb hello extra"}',
])
def test_malformed_hello_is_not_a_legacy_escape_hatch(answer):
    from hermes_cli.worker.transport import NegotiatedTransport, Reply

    raw = Wire(Reply(0, answer))

    reply = NegotiatedTransport(raw, "broken").call(["cancel", "OT-PROTOCOL"])

    _refusal(reply)
    assert raw.received == [(["hello"], b"")]


def test_hello_timeout_fails_closed_even_for_recovery():
    from hermes_cli.worker.transport import NegotiatedTransport, Reply

    raw = Wire(Reply(124, b"", "far: no answer"))

    reply = NegotiatedTransport(raw, "far").call(["meta", "OT-PROTOCOL"])

    _refusal(reply)
    assert raw.received == [(["hello"], b"")]


def test_ssh_failure_during_hello_remains_unreachable_in_devices(tmp_path, monkeypatch):
    from hermes_cli import lina_devices
    from hermes_cli.worker.settings import Device
    from hermes_cli.worker.transport import NegotiatedTransport, Reply

    raw = Wire(Reply(255, b"", "connection refused"))
    contact = NegotiatedTransport(raw, "far")
    monkeypatch.setattr(lina_devices, "for_device", lambda *a: contact)

    result = lina_devices.probe(Device("far", "remote"), tmp_path, "omo")

    assert result["ssh"] is False
    assert result["protocol_error"] is True
    assert raw.received == [(["hello"], b"")]


def test_refresh_returns_a_reply_and_replaces_the_descriptor():
    from hermes_cli.worker.transport import NegotiatedTransport, Reply

    raw = Wire(_reply(_descriptor(instance="first")))
    transport = NegotiatedTransport(raw, "far")
    assert transport.refresh().code == 0
    raw.hello = _reply(_descriptor(instance="second"))

    reply = transport.refresh()

    assert isinstance(reply, Reply) and reply.code == 0
    assert transport.descriptor["instance_id"] == "second"


def test_new_contact_renegotiates_instead_of_reusing_a_previous_peer_result():
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply(_descriptor()))
    assert NegotiatedTransport(raw, "far").call(["probe"]).code == 0
    raw.hello = _reply(_descriptor(2, 2))

    reply = NegotiatedTransport(raw, "far").call(["probe"])

    _refusal(reply)
    assert [verb for verb, _ in raw.received] == [["hello"], ["probe"], ["hello"]]


@pytest.mark.parametrize("host,raw_class", [("local", "LocalTransport"), ("remote", "SshTransport")])
def test_for_device_negotiates_both_local_and_ssh_without_executing_commands(tmp_path, monkeypatch, host, raw_class):
    from hermes_cli.worker import transport
    from hermes_cli.worker.settings import Device
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply(_descriptor(2, 2)))
    monkeypatch.setattr(getattr(transport, raw_class), "call", lambda self, *a, **kw: raw.call(*a, **kw))

    contact = transport.for_device(Device("peer", host), tmp_path / "worker")
    reply = contact.call(["put", "OT-PROTOCOL", "prompt.1"], b"blocked")

    assert isinstance(contact, NegotiatedTransport)
    _refusal(reply)
    assert raw.received == [(["hello"], b"")]


@pytest.mark.parametrize("action", ["stop", "restart"])
def test_service_action_uses_only_fake_stop_start_and_preserves_tasks(tmp_path, monkeypatch, action):
    from hermes_cli.worker import protocol, service

    root = tmp_path / "worker"
    instance = protocol.begin_instance(root)
    (root / "serve.heartbeat").write_text(f"{time.time()}\n")
    task = root / "tasks" / "OT-PROTOCOL" / "prompt.1"
    task.parent.mkdir(parents=True)
    task.write_text("running work survives service control")
    effects = []

    def stop(base=None, out=print):
        assert Path(base) == root.parent
        effects.append("stop")
        (root / "serve.heartbeat").unlink(missing_ok=True)
        return 0

    def start(base=None, out=print):
        assert Path(base) == root.parent
        effects.append("start")
        protocol.begin_instance(root)
        (root / "serve.heartbeat").write_text(f"{time.time()}\n")
        return 0

    monkeypatch.setattr(service, "stop", stop)
    monkeypatch.setattr(service, "start", start)

    result = protocol.service_action(root, action, instance)

    assert result.get("action") != "error"
    assert effects == (["stop", "start"] if action == "restart" else ["stop"])
    assert task.read_text() == "running work survives service control"
    if action == "restart":
        assert protocol.hello(root)["instance_id"] != instance


@pytest.mark.parametrize("recorded_id", ["previous-instance", ""])
def test_device_service_refuses_stale_or_empty_identity_without_stopping(tmp_path, monkeypatch, recorded_id):
    from hermes_cli.worker import protocol, service

    root = tmp_path / "worker"
    current = protocol.begin_instance(root)
    (root / "serve.heartbeat").write_text(f"{time.time()}\n")
    effects = []
    monkeypatch.setattr(service, "stop", lambda *a, **kw: effects.append("stop") or 0)
    monkeypatch.setattr(service, "start", lambda *a, **kw: effects.append("start") or 0)

    result = protocol.service_action(root, "restart", recorded_id)

    assert result["action"] == "error" and result["stale_instance"] is True
    assert result["reason"]
    assert effects == []
    assert json.loads((root / "serve.instance.json").read_text())["instance_id"] == current


def test_transport_service_refreshes_before_refusing_a_stale_recorded_identity():
    from hermes_cli.worker.transport import NegotiatedTransport

    raw = Wire(_reply(_descriptor(instance="old")))
    contact = NegotiatedTransport(raw, "far")
    assert contact.refresh().code == 0
    raw.hello = _reply(_descriptor(instance="new"))

    reply = contact.service("restart", "old")

    _refusal(reply)
    assert contact.descriptor["instance_id"] == "new"
    assert [verb for verb, _ in raw.received] == [["hello"], ["hello"]]


def test_transport_service_success_reaches_real_dispatch_with_the_recorded_id(tmp_path, monkeypatch):
    from hermes_cli.worker import protocol, service
    from hermes_cli.worker.transport import LocalTransport, NegotiatedTransport

    root = tmp_path / "worker"
    instance = protocol.begin_instance(root)
    (root / "serve.heartbeat").write_text(f"{time.time()}\n")
    effects = []

    def stop(base=None, out=print):
        effects.append(Path(base))
        (root / "serve.heartbeat").unlink()
        return 0

    monkeypatch.setattr(service, "stop", stop)

    reply = NegotiatedTransport(LocalTransport(root), "here").service("stop", instance)

    assert reply.code == 0 and reply.json().get("action") != "error"
    assert effects == [root.parent]
    assert not (root / "serve.heartbeat").exists()


def test_node_stale_instance_refreshes_descriptor_without_replaying_service(tmp_path, monkeypatch):
    from hermes_cli.worker import device, protocol, service
    from hermes_cli.worker.transport import NegotiatedTransport, Reply

    root = tmp_path / "worker"
    instance = protocol.begin_instance(root)
    (root / "serve.heartbeat").write_text(f"{time.time()}\n")
    effects, wire_verbs = [], []
    monkeypatch.setattr(service, "stop", lambda *a, **kw: effects.append("stop") or 0)
    monkeypatch.setattr(service, "start", lambda *a, **kw: effects.append("start") or 0)

    class RacingWire:
        def call(self, verb, stdin=b"", timeout=120):
            wire_verbs.append(verb[0])
            if verb[0] == "service":
                protocol.begin_instance(root)
                (root / "serve.heartbeat").write_text(f"{time.time()}\n")
            code, out = device.dispatch(root, verb, stdin)
            return Reply(code, out)

    contact = NegotiatedTransport(RacingWire(), "here")

    reply = contact.service("restart", instance)

    assert reply.code != 0 and reply.json()["stale_instance"] is True
    assert effects == []
    assert wire_verbs.count("service") == 1
    assert wire_verbs[-1] == "hello"
    assert contact.descriptor["instance_id"] != instance
    assert contact.descriptor["instance_id"] == protocol.hello(root)["instance_id"]


def test_protocol_refusal_reason_survives_bidding_devices_and_display(tmp_path, monkeypatch):
    from hermes_cli import lina_devices
    from hermes_cli.worker import bidding, settings, transport
    from hermes_cli.worker.settings import Device
    from hermes_cli.worker.transport import NegotiatedTransport

    root = tmp_path / "worker"
    dev = Device("far", "remote", auto=False, no_repo=True)
    contact = NegotiatedTransport(Wire(_reply(_descriptor(2, 2))), dev.name)
    reason = _refusal(contact.call(["probe"]))
    monkeypatch.setattr(transport, "for_device", lambda *a, **kw: contact)
    monkeypatch.setattr(lina_devices, "for_device", lambda *a, **kw: contact)
    monkeypatch.setattr(settings, "section", lambda base=None: {"default_runner": "fake"})

    bid_probe = bidding.probe_all([dev], root, repo="owner/repo", runner="fake")[0]
    device_probe = lina_devices.probe(dev, root, "fake")
    display = lina_devices.readiness(dev, root)

    for found in (bid_probe, device_probe):
        assert found["protocol_error"] is True
        assert bidding.readiness(dev, found, repo="owner/repo", min_free_gb=5) == reason
    winner, rows = bidding.choose([dev], [bid_probe], repo="owner/repo")
    assert winner is None and rows[0]["why"] == reason
    assert display["ready"] is False and display["why"] == reason
    assert reason in lina_devices.render(display)
