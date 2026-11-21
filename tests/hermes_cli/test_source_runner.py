"""Manual collector safety contracts across independent invocations (Refs #556, #818)."""

import json
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from datetime import UTC, datetime, timedelta
from threading import Barrier

import pytest

import hermes_constants
from hermes_cli.sources import adapters, runner
from hermes_cli.sources.runner import RunOptions, resume, run, status
from hermes_cli.sources.state import SourceState, StateError
from utils import atomic_roundtrip_yaml_update


@pytest.fixture
def source(tmp_path, monkeypatch):
    monkeypatch.setattr(hermes_constants, "get_default_hermes_root", lambda: tmp_path)
    config = tmp_path / "config.yaml"
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.mail", {
        "enabled": True, "accounts": ["sample-box"],
    })
    clock = [datetime(2026, 1, 5, tzinfo=UTC).timestamp()]
    monkeypatch.setattr(runner, "time", lambda: clock[0])
    # Every test starts with a disconnected transport, even those exercising a zero-call gate.
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: pytest.fail("unexpected transport call"))
    return RunOptions(state_dir=tmp_path / "state", spool=tmp_path / "spool" / "events"), clock, config


def test_auth_denial_stops_after_one_and_next_run_makes_zero_calls(source, monkeypatch):
    options, clock, _ = source
    calls = []

    def denied(kind, tool, params, account=None):
        calls.append(tool)
        raise adapters.SourceError("auth", auth=True)

    monkeypatch.setattr(adapters, "invoke", denied)
    first = run("mail", replace(options, dry_run=True))
    clock[0] += 3601
    second = run("mail", replace(options, probe=True))

    assert [first.calls, second.calls] == [1, 0]
    assert first.outcome == second.outcome == "stopped"
    assert len(calls) == 1
    snapshot = status("mail", options.state_dir)
    assert snapshot["last_error"] == "auth" and snapshot["error_time"] == clock[0] - 3601
    assert not options.spool.exists()


def test_rate_limit_stops_each_attempt_and_third_failure_requires_resume(source, monkeypatch):
    options, clock, config = source
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.mail.max_calls_per_day", 3)
    calls = []

    def limited(kind, tool, params, account=None):
        calls.append(tool)
        raise adapters.SourceError("rate_limited")

    monkeypatch.setattr(adapters, "invoke", limited)
    outcomes = []
    for _ in range(4):
        result = run("mail", options)
        outcomes.append((result.outcome, result.calls))
        clock[0] += 3600
    resume("mail", options.state_dir)

    assert outcomes == [("error", 1), ("error", 1), ("stopped", 1), ("stopped", 0)]
    assert run("mail", options).outcome == "day_limit"
    assert len(calls) == 3
    clock[0] += 20 * 3600
    assert run("mail", options).calls == 1


def test_interrupted_transport_is_precharged_and_interval_applies(source, monkeypatch):
    options, clock, config = source
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.mail.max_calls_per_day", 1)
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.mail.min_interval_minutes", 1)

    def interrupt(*args, **kwargs):
        raise KeyboardInterrupt

    monkeypatch.setattr(adapters, "invoke", interrupt)
    with pytest.raises(KeyboardInterrupt):
        run("mail", options)
    clock[0] += 3599
    assert run("mail", options).outcome == "not_due"
    clock[0] += 1
    assert run("mail", options).outcome == "day_limit"


@pytest.mark.parametrize(("key", "value", "outcome"), [
    ("memory.external_messages.enabled", False, "disabled"),
    ("memory.external_messages.sources.mail.enabled", False, "disabled"),
    ("memory.external_messages.sources.mail.accounts", [], "no_accounts"),
    ("memory.external_messages.sources.mail.query", "", "no_query"),
])
def test_switches_and_empty_alias_list_make_no_calls(source, key, value, outcome):
    options, _, config = source
    atomic_roundtrip_yaml_update(config, key, value)
    assert run("mail", options).outcome == outcome
    assert not options.state_dir.exists() and not options.spool.exists()


def _paged(kind, config, since, until, rooms, call, emit, checkpoint, *, probe=False):
    while True:
        call(min(adapters.TOOLS[kind]), {})


@pytest.mark.parametrize(("kind", "override", "expected"), [("mail", 1000, 10), ("slack", 1000, 15), ("mail", 2, 2)])
def test_config_and_cli_cannot_raise_code_caps(source, monkeypatch, kind, override, expected):
    options, _, config = source
    atomic_roundtrip_yaml_update(config, f"memory.external_messages.sources.{kind}.enabled", True)
    atomic_roundtrip_yaml_update(config, f"memory.external_messages.sources.{kind}.max_calls_per_run", 999)

    calls = []
    monkeypatch.setattr(adapters, "collect", _paged)
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: calls.append(a[1]) or {})
    result = run(kind, replace(options, max_calls=override))
    assert result.outcome == "run_limit" and result.calls == len(calls) == expected


def test_hourly_usage_rolls_from_each_call_time(source, monkeypatch):
    options, clock, config = source
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.mail.max_calls_per_hour", 2)
    monkeypatch.setattr(adapters, "collect", _paged)

    def slow_read(*args, **kwargs):
        clock[0] += 30
        return {}

    monkeypatch.setattr(adapters, "invoke", slow_read)
    start = clock[0]
    assert run("mail", options).calls == 2
    clock[0] = start + 3600
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: {})
    result = run("mail", options)
    assert (result.calls, result.outcome) == (1, "hour_limit")


def test_concurrent_manual_invocations_share_one_budget(source, monkeypatch):
    options, _, _ = source
    barrier = Barrier(2)
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: {"messages": []})

    def probe():
        barrier.wait(timeout=10)
        return run("mail", replace(options, probe=True)).calls

    with ThreadPoolExecutor(max_workers=2) as pool:
        reads = [pool.submit(probe) for _ in range(2)]
        assert sorted(f.result(timeout=10) for f in reads) == [0, 1]
    assert status("mail", options.state_dir)["calls_day"] == 1


def _one_room(kind, config, since, until, rooms, call, emit, checkpoint, *, probe=False):
    call("GMAIL_FETCH_EMAILS", {}, "sample-box")
    emit({"text": "synthetic fixture"})
    checkpoint("sample-box", until.isoformat(), since.isoformat())


def test_dry_run_counts_and_preserves_spool_and_room_coverage(source, monkeypatch):
    options, clock, _ = source
    monkeypatch.setattr(adapters, "collect", _one_room)
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: {})
    start = datetime.fromtimestamp(clock[0], UTC)
    success = run("mail", replace(options, since=start - timedelta(days=5)))
    path = options.state_dir / "mail.json"
    coverage = SourceState.load(path)
    assert coverage.rooms == {"sample-box": {"cursor": start.isoformat(), "since": (start - timedelta(days=2)).isoformat()}}
    assert success.messages == 1 and coverage.last_success == clock[0]
    before = options.spool.read_bytes()
    clock[0] += 3600
    dry = run("mail", replace(options, dry_run=True))
    after = SourceState.load(path)
    assert (dry.calls, dry.messages) == (1, 1)
    assert options.spool.read_bytes() == before and after.rooms == coverage.rooms
    assert after.last_success == coverage.last_success and len(after.calls) == 2


def test_contiguous_empty_reads_extend_coverage_but_narrow_since_starts_new_range(source, monkeypatch):
    options, clock, _ = source
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: {"messages": []})
    first = datetime.fromtimestamp(clock[0], UTC)
    run("mail", options)
    clock[0] += 3600
    run("mail", options)
    path = options.state_dir / "mail.json"
    assert SourceState.load(path).rooms["sample-box"] == {
        "cursor": (first + timedelta(hours=1)).isoformat(), "since": (first - timedelta(days=2)).isoformat(),
    }
    clock[0] += 3600
    narrowed = first + timedelta(minutes=90)
    run("mail", replace(options, since=narrowed))
    assert SourceState.load(path).rooms["sample-box"]["since"] == narrowed.isoformat()
    summary = json.dumps(status("mail", options.state_dir))
    assert "sample-box" not in summary and '"rooms": 1' in summary


def test_capped_room_resumes_after_durable_append_without_claiming_coverage(source, monkeypatch):
    options, clock, _ = source
    stamp = int(clock[0] - 1800) * 1000
    detail_reads = []

    def two_messages(kind, tool, params, account=None):
        if tool == "GMAIL_FETCH_EMAILS":
            return {"messages": [{"id": "0"}, {"id": "1"}]}
        native = params["message_id"]
        detail_reads.append(native)
        return {"id": native, "threadId": "thread", "internalDate": stamp}

    monkeypatch.setattr(adapters, "invoke", two_messages)
    first = run("mail", replace(options, max_calls=2))
    path = options.state_dir / "mail.json"
    room = SourceState.load(path).rooms["sample-box"]
    assert first.outcome == "run_limit" and room.keys() == {"pending"}
    clock[0] += 3600
    assert run("mail", replace(options, dry_run=True)).messages == 1
    assert SourceState.load(path).rooms["sample-box"] == room
    clock[0] += 3600
    assert run("mail", options).messages == 1
    assert SourceState.load(path).rooms["sample-box"] == {
        "since": room["pending"]["since"], "cursor": room["pending"]["until"],
    }
    events = [json.loads(line) for line in options.spool.read_text().splitlines()]
    assert [event["messages"][0]["native_id"] for event in events] == ["0", "1"]
    assert detail_reads == ["0", "1", "1"]


def test_slack_capped_thread_resumes_without_emitting_parent_twice(source, monkeypatch):
    options, clock, config = source
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.slack.enabled", True)
    root, reply = str(int(clock[0]) - 90), str(int(clock[0]) - 60)

    def thread(kind, tool, params, account=None):
        if tool == "SLACK_LIST_ALL_CHANNELS":
            return {"channels": [{"id": "fixture-room", "is_member": True}]}
        if tool == "SLACK_FETCH_CONVERSATION_HISTORY":
            return {"messages": [{"ts": root, "reply_count": 1, "permalink": "https://example.invalid/root"}]}
        if tool == "SLACK_FETCH_MESSAGE_THREAD_FROM_A_CONVERSATION":
            return {"messages": [{"ts": reply, "thread_ts": root, "permalink": "https://example.invalid/reply"}]}
        pytest.fail("unexpected tool")

    monkeypatch.setattr(adapters, "invoke", thread)
    partial = run("slack", replace(options, max_calls=2))
    assert (partial.outcome, partial.messages) == ("run_limit", 1)
    clock[0] += 3600
    assert run("slack", options).messages == 1
    events = [json.loads(line) for line in options.spool.read_text().splitlines()]
    assert [event["messages"][0]["native_id"] for event in events] == [root, reply]
    assert status("slack", options.state_dir)["rooms"] == 1


def test_spool_failure_keeps_previous_cursor_and_sanitizes_error(source, monkeypatch, capsys):
    from hermes_cli.sources import spool
    from hermes_cli.sources.cli import build_sources_parser
    import argparse

    options, clock, _ = source
    monkeypatch.setattr(adapters, "collect", _one_room)
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: {})
    run("mail", options)
    path = options.state_dir / "mail.json"
    previous = SourceState.load(path).rooms
    clock[0] += 3600

    def fail(*args):
        raise OSError("synthetic-private-payload")

    monkeypatch.setattr(spool, "append_message", fail)
    parser = argparse.ArgumentParser()
    build_sources_parser(parser.add_subparsers())
    args = parser.parse_args(["sources", "collect", "mail", "--spool", str(options.spool),
                              "--state-dir", str(options.state_dir)])
    with pytest.raises(SystemExit) as stopped:
        args.func(args)
    assert stopped.value.code == 1 and SourceState.load(path).rooms == previous
    assert "synthetic-private-payload" not in capsys.readouterr().out + path.read_text()


def test_registered_probe_reads_once_without_collection_advancement(source, monkeypatch, capsys):
    from hermes_cli.main import _build_cli_parser

    options, _, _ = source
    calls = []
    monkeypatch.setattr(adapters, "invoke", lambda *a, **kw: calls.append(a[1]) or {"messages": []})
    parser, _ = _build_cli_parser()
    args = parser.parse_args(["sources", "probe", "mail", "--state-dir", str(options.state_dir)])
    args.func(args)
    assert json.loads(capsys.readouterr().out.splitlines()[-1])["outcome"] == "ok"
    assert calls == ["GMAIL_FETCH_EMAILS"]
    state = SourceState.load(options.state_dir / "mail.json")
    assert state.last_probe_success and state.last_success is None and not state.rooms
    assert not options.spool.exists()


def test_corrupt_state_and_overlapping_overrides_fail_before_transport(source):
    options, _, _ = source
    options.state_dir.mkdir(mode=0o700)
    state = options.state_dir / "mail.json"
    state.write_text("{}")
    with pytest.raises(StateError):
        run("mail", options)
    with pytest.raises(StateError):
        run("mail", replace(options, spool=options.state_dir / "slack.lock"))
