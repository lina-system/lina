"""The node side of the daily runner sync (lina#238), and the parts the hub and node share: the moved pins laid over
the repo pins, the ``runner-sync`` verb, what ``hello`` reports for it, the version rules, the answer of a
trial, and the once-a-day start from ``lina worker serve``.

The runner is the real Grok pin; its "release" is a Python script handed to a fake fetch. Nothing reaches the network
and no real service, ssh or magpie is touched.
"""

from __future__ import annotations

import hashlib
import json
import sys
import time

import pytest

from hermes_cli import lina_deps, lina_runner_sync_daily, lina_runners
from hermes_cli import lina_runner_sync_state as state
from hermes_cli import lina_runner_sync_trial as trials
from hermes_cli.worker import device, protocol

URL = "https://x.ai/cli/grok-1.0.71-linux-x86_64"


def release(version: str, answer: str = "OK") -> bytes:
    return (f"#!{sys.executable}\nimport sys\nif '--version' in sys.argv:\n    print('grok {version} (fake)')\n"
            f"    sys.exit(0)\nprint({answer!r})\n").encode()


@pytest.fixture
def node(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    vendor = lina_runners.vendor_dir(root, "grok")
    (vendor / "1.0.70").mkdir(parents=True)
    (vendor / "1.0.70" / "grok").write_bytes(release("1.0.70"))
    (vendor / "1.0.70" / "grok").chmod(0o755)
    lina_deps.set_current(root, "1.0.70", vendor)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: root)
    monkeypatch.setattr(lina_deps, "platform_key", lambda: "linux-x64")
    served = {URL: release("1.0.71")}
    monkeypatch.setattr(lina_runners, "download", lambda url, dest: dest.write_bytes(served[url]))
    return {"root": root, "worker": root / "worker", "served": served}


def payload(sha256: str | None = None) -> bytes:
    digest = sha256 or hashlib.sha256(release("1.0.71")).hexdigest()
    return json.dumps({"runners": {"grok": {"version": "1.0.71",
                                            "artifacts": {"linux-x64": {"url": URL, "sha256": digest}}}}}).encode()


def sync_verb(node, data: bytes) -> tuple[int, dict]:
    code, out = device.dispatch(node["worker"], ["runner-sync"], data)
    return code, json.loads(out)


def test_only_the_runners_follow_and_each_source_names_the_pinned_artifacts(tmp_path):
    table = lina_runners.pins(root=tmp_path, moved=False)
    followed = {name for name, pin in table.items() if pin.follow}
    assert followed == {"omo", "codex", "claude", "dsh", "grok", "cursor-agent", "agy"}, \
        "The ACP adapters keep LINA's pins; omo follows like every tool (lina#531)"
    assert table["claude"].follow == {"npm": "stable"}
    for name in followed:
        pin = table[name]
        assert pin.trial.get("argv") or pin.trial.get("acp"), name
        if pin.installer == "npm":
            continue
        assert set(pin.follow["platforms"]) == set(pin.artifacts), name
        if "artifact" in pin.follow:
            for key, artifact in pin.artifacts.items():
                assert pin.follow["artifact"].format(version=pin.version, platform=pin.follow["platforms"][key]) \
                    == artifact["url"], (name, key)
    import re
    cursor = table["cursor-agent"]
    found = re.search(cursor.follow["pattern"], f"curl -fsSL {cursor.artifacts['linux-x64']['url']}")
    assert found and found.group(1) == cursor.version


def test_moved_pins_lie_over_the_repo_pins_and_the_repo_pin_is_the_floor(tmp_path):
    repo = lina_runners.pins(root=tmp_path, moved=False)["codex"]
    (tmp_path / state.OVERLAY).write_text(json.dumps({
        "codex": {"version": "999.0.0", "integrity": "sha512-moved"},
        "grok": {"version": "0.0.1", "artifacts": {}}}))

    table = lina_runners.pins(root=tmp_path)

    assert (table["codex"].version, table["codex"].integrity) == ("999.0.0", "sha512-moved")
    assert table["grok"] == lina_runners.pins(root=tmp_path, moved=False)["grok"], "below the repo pin: ignored"
    assert lina_runners.pins(root=tmp_path, moved=False)["codex"] == repo
    assert state.pin_from(tmp_path, "codex") == "sync" and state.pin_from(tmp_path, "claude") == "repo"


def test_the_verb_installs_the_moved_pin_and_keeps_the_way_back(node):
    code, reply = sync_verb(node, payload())

    root = node["root"]
    assert code == 0 and reply["results"]["grok"] == {"state": "switched", "version": "1.0.71", "previous": "1.0.70"}
    runner = lina_runners.pins(root=root)["grok"]
    assert lina_runners.current_version(root, runner) == "1.0.71"
    assert lina_runners.shim_path(root, "grok").exists()
    history = [json.loads(line) for line in (root / state.HISTORY).read_text().splitlines()]
    assert history[-1]["before"] is None and history[-1]["after"]["version"] == "1.0.71"
    assert sync_verb(node, payload())[1]["results"]["grok"]["state"] == "same"


def test_a_hash_mismatch_on_the_node_keeps_nothing_and_its_pin(node):
    code, reply = sync_verb(node, payload(sha256="0" * 64))

    root = node["root"]
    assert code == 0 and reply["results"]["grok"]["state"] == "failed"
    assert "sha256 mismatch" in reply["results"]["grok"]["reason"]
    assert "grok" not in state.overlay(root), "the node's pin goes back to what it runs"
    assert lina_runners.current_version(root, lina_runners.pins(root=root)["grok"]) == "1.0.70"
    assert not (lina_runners.vendor_dir(root, "grok") / "1.0.71").exists()


def test_a_node_with_a_run_holding_a_slot_refuses_before_it_installs(node):
    task = node["worker"] / "tasks" / "OT-WORK"
    task.mkdir(parents=True)
    (task / "request.1").write_text("{}")

    code, reply = sync_verb(node, payload())

    assert code == 0 and reply["results"]["grok"]["state"] == "busy"
    assert not (node["root"] / state.OVERLAY).exists()
    assert not (lina_runners.vendor_dir(node["root"], "grok") / "1.0.71").exists()


def test_a_runner_the_node_never_installed_is_only_pinned(node):
    import shutil
    shutil.rmtree(lina_runners.vendor_dir(node["root"], "grok"))

    _, reply = sync_verb(node, payload())

    assert reply["results"]["grok"]["state"] == "prepare"
    assert state.overlay(node["root"])["grok"]["version"] == "1.0.71"
    assert not (lina_runners.vendor_dir(node["root"], "grok") / "1.0.71").exists()


@pytest.mark.parametrize("data", [
    {"runners": {"claude-agent-acp": {"version": "1.0.0", "integrity": "sha512-x"}}},
    {"runners": {"codex": {"version": "1.0.0"}}},
    {"runners": {"grok": {"version": "1.0.71", "artifacts": {"linux-x64": {"url": URL}}}}},
    {"runners": {}},
])
def test_the_verb_refuses_what_the_sync_does_not_move_or_moves_without_a_hash(node, data):
    code, reply = sync_verb(node, json.dumps(data).encode())
    assert code == 1 and reply["action"] == "error"
    assert not (node["root"] / state.OVERLAY).exists()


def test_hello_offers_the_verb_and_reports_platform_and_proxy(node):
    hello = protocol.hello(node["worker"])
    caps = hello["capabilities"]
    assert "runner-sync" in caps["verbs"]
    assert caps["platform"] == "linux-x64"
    assert set(caps["proxy"]) == {"url", "health", "models", "remote_id", "service", "version", "names"}


@pytest.mark.parametrize(("new", "old", "order", "major"), [
    ("1.0.71", "1.0.70", 1, False),
    ("1.0.69", "1.0.70", -1, False),
    ("3.0.0", "2.9.9", 1, True),
    ("1.0.0", "0.9.0", 1, True),
    ("0.2.1", "0.2.1-rc.5", 1, False),
    ("0.2.1-rc.10", "0.2.1-rc.9", 1, False),
    ("0.166.0-alpha.6", "0.166.0-alpha.5.1", 1, False),
    ("2027.01.02-abc", "2026.11.05-def", 1, False),
    ("2026.11.05-abc", "2026.11.05-def", 1, False),
])
def test_versions_order_and_major_jumps(new, old, order, major):
    assert state.compare(new, old) == order
    assert state.major_jump(new, old) is major


def _installed_runner(root, trial: dict) -> lina_runners.RunnerPin:
    runner = lina_runners.RunnerPin(name="grok", installer="binary", version="1.0.71", bin="grok", sign_in="grok",
                                    entry="grok", trial=trial)
    folder = lina_runners.vendor_dir(root, "grok") / "1.0.71"
    folder.mkdir(parents=True)
    (folder / "grok").write_bytes(release("1.0.71"))
    (folder / "grok").chmod(0o755)
    return runner



def test_a_trial_only_needs_the_answer(tmp_path):
    runner = _installed_runner(tmp_path, {"argv": ["-p", "{prompt}"]})
    assert trials.trial(tmp_path, runner, "1.0.71") == \
        (True, "answered OK")


_FAKE_ACP = '''#!{python}
import json, sys
for line in sys.stdin:
    msg = json.loads(line)
    if msg["method"] == "initialize":
        result = {{"protocolVersion": 1}}
    elif msg["method"] == "session/new":
        result = {{"sessionId": "s1"}}
    else:
        update = {{"sessionUpdate": "agent_message_chunk", "content": {{"type": "text", "text": {answer!r}}}}}
        print(json.dumps({{"jsonrpc": "2.0", "method": "session/update",
                          "params": {{"sessionId": "s1", "update": update}}}}), flush=True)
        result = {{"stopReason": "end_turn"}}
    print(json.dumps({{"jsonrpc": "2.0", "id": msg["id"], "result": result}}), flush=True)
'''


@pytest.mark.parametrize(("answer", "ok"), [("OK\\n", True), ("Sure thing", False)])
def test_an_acp_trial_asks_once_over_stdio(tmp_path, answer, ok):
    runner = _installed_runner(tmp_path, {"acp": ["acp"]})
    entry = lina_runners.entry_path(tmp_path, runner, "1.0.71")
    entry.write_text(_FAKE_ACP.format(python=sys.executable, answer=answer.replace("\\n", "\n")))

    assert trials.trial(tmp_path, runner, "1.0.71")[0] is ok


class _Ended:
    def poll(self):
        return 0


@pytest.fixture(params=["Asia/Seoul", "UTC"])
def zone(request, monkeypatch):
    monkeypatch.setenv("TZ", request.param)
    time.tzset()
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)
    yield request.param
    monkeypatch.undo()
    time.tzset()


def _local(day_offset: int, hour: int, minute: int = 0) -> float:
    today = time.localtime(1_800_000_000)
    return time.mktime((today.tm_year, today.tm_mon, today.tm_mday + day_offset, hour, minute, 0, 0, 0, -1))


def test_the_daily_sync_starts_once_a_day_after_its_time(tmp_path, zone, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text('worker:\n  runner_sync: {at: "04:00"}\n', encoding="utf-8")
    started: list[float] = []

    def start(at):
        return lina_runner_sync_daily.maybe_start(base / "worker", now=at,
                                                  spawn=lambda root, only, run: started.append(at) or _Ended())

    assert start(_local(0, 3, 59)) is False
    assert start(_local(0, 4, 0)) is True
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)  # the service restarted
    assert start(_local(0, 23, 59)) is False, "a restart does not start it twice on one day"
    assert start(_local(1, 4, 30)) is True
    assert started == [_local(0, 4, 0), _local(1, 4, 30)]
    assert state.daily(base)["started"] == _local(1, 4, 30)


def test_a_daily_start_that_raises_stays_inside_the_tick_and_is_an_issue(tmp_path, zone):
    from hermes_cli import lina_runner_sync
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text('worker:\n  runner_sync: {at: "04:00"}\n', encoding="utf-8")
    calls: list[float] = []

    def broken(root, only, run):
        calls.append(1)
        raise OSError("no python here")

    assert lina_runner_sync_daily.maybe_start(base / "worker", now=_local(0, 5), spawn=broken) is False
    assert lina_runner_sync_daily.maybe_start(base / "worker", now=_local(0, 6), spawn=broken) is False
    assert calls == [1], "today's start is spent: not tried again the same day"
    daily = state.daily(base)
    assert (daily["rc"], daily["finished"], daily["error"]) == (1, _local(0, 5), "could not start: OSError: no python here")
    assert "could not start: OSError: no python here" in (state.sync_dir(base) / "sync.log").read_text()
    issues, _ = lina_runner_sync.issue_lines(base)
    assert issues == ["- daily: could not start: OSError: no python here"]


def test_the_daily_sync_is_off_without_its_setting(tmp_path):
    base = tmp_path / "lina"
    base.mkdir()
    assert lina_runner_sync_daily.maybe_start(base / "worker", now=_local(0, 12),
                                              spawn=lambda root, only, run: pytest.fail("off by default")) is False
    assert not state.last_path(base).exists()
