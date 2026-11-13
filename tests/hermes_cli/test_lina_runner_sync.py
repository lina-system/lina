"""The daily runner sync on the hub (lina#238): a runner's pin follows its source after one real request on the hub,
and the nodes follow the same day; a failure holds the runner until a person clears it, a major jump waits, a
source that goes back is refused, a node with work is skipped, and ``--plan`` writes nothing.

The runner is the real Grok pin (``binary`` installer, channel source). Its "release" is a Python script served by
a fake HTTP source: the hub really downloads it, checks its hash, installs it beside the current one, asks it
``--version`` and one prompt, and switches through the shim. Nodes are fakes that record what the hub sends, except
where a node's own ``runner-sync`` verb runs for real. Nothing reaches the network.
"""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_deps, lina_runner_sync, lina_runners
from hermes_cli import lina_runner_sync_state as state
from hermes_cli.lina_runner_sync import Env, NodeView
from hermes_cli.worker.transport import LocalTransport, Reply

CHANNEL = "https://x.ai/cli/stable"
SECRET = "sk-test-secret-value-0042"


def release(version: str, answer: str = "OK", rc: int = 0) -> bytes:
    return (f"#!{sys.executable}\nimport sys\nif '--version' in sys.argv:\n    print('grok {version} (fake)')\n"
            f"    sys.exit(0)\nprint({answer!r})\nsys.exit({rc})\n").encode()


class FakeHttp:
    def __init__(self):
        self.texts: dict[str, str] = {}
        self.files: dict[str, tuple[bytes, dict]] = {}
        self.downloads: list[str] = []

    def publish(self, version: str, body: bytes, headers: dict | None = None) -> None:
        self.texts[CHANNEL] = version + "\n"
        self.files[f"https://x.ai/cli/grok-{version}-linux-x86_64"] = (body, headers or {})

    def text(self, url: str) -> str:
        return self.texts[url]

    def file(self, url: str, dest: Path) -> dict:
        self.downloads.append(url)
        body, headers = self.files[url]
        dest.write_bytes(body)
        return headers


class FakeNode:
    def __init__(self, name: str, installed: str = "1.0.70", *, running: int = 0, verbs=("hello", "runner-sync"),
                 answer: str = "switched"):
        self.name, self.installed, self.running, self.verbs, self.answer = name, installed, running, verbs, answer
        self.sent: list[dict] = []

    def view(self) -> NodeView:
        row = {"name": "grok", "pin": "grok", "pinned": self.installed, "installed": self.installed,
               "sign_in": "signed_in"}
        return NodeView(self.name, True, platform="linux-x64", verbs=tuple(self.verbs), rows={"grok": row},
                        running=self.running, proxy={"url": "http://127.0.0.1:3425/v1/models", "health": 200, "models": 2},
                        call=self.call)

    def call(self, verb, data, timeout):
        payload = json.loads(data)
        self.sent.append(payload)
        version = payload["runners"]["grok"]["version"]
        result = {"state": self.answer, "version": version}
        if self.answer == "failed":
            result["reason"] = "grok answered nothing through the shim"
        elif self.answer == "switched":
            self.installed = version
        return Reply(0, json.dumps({"action": "runner-sync", "results": {"grok": result}}).encode())


@pytest.fixture
def hub(tmp_path, monkeypatch):
    return make_hub(tmp_path, monkeypatch)


def make_hub(tmp_path, monkeypatch) -> dict:
    root = tmp_path / "hub"
    vendor = lina_runners.vendor_dir(root, "grok")
    (vendor / "1.0.70").mkdir(parents=True)
    entry = vendor / "1.0.70" / "grok"
    entry.write_bytes(release("1.0.70"))
    entry.chmod(0o755)
    lina_deps.set_current(root, "1.0.70", vendor)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: root)
    monkeypatch.setattr(lina_deps, "platform_key", lambda: "linux-x64")
    http = FakeHttp()
    nodes: list[FakeNode] = []
    notices: list[str] = []
    env = Env(http=http, fetch=lambda url, dest: pytest.fail(f"unexpected fetch {url}"),

              nodes=lambda root: [node.view() for node in nodes], sign_in=lambda runner: "signed_in",
              notify=lambda root, text: notices.append(text) or "kept")
    return {"root": root, "http": http, "nodes": nodes, "env": env, "notices": notices}


def run(hub, **kwargs) -> tuple[int, list[str]]:
    lines: list[str] = []
    code = lina_runner_sync.sync(hub["root"], only=["grok"], env=hub["env"], out=lines.append, **kwargs)
    return code, lines


def current(root: Path) -> str | None:
    return lina_runners.current_version(root, lina_runners.pins(root=root)["grok"])


def files(root: Path) -> dict[str, bytes]:
    return {str(p.relative_to(root)): p.read_bytes() for p in sorted(root.rglob("*")) if p.is_file()}


def test_a_newer_version_is_tried_on_the_hub_then_the_nodes_follow_the_same_day(hub):
    hub["http"].publish("1.0.71", release("1.0.71"))
    hub["nodes"].extend([FakeNode("build-a"), FakeNode("build-b")])

    code, _ = run(hub)

    root = hub["root"]
    assert code == 0
    assert current(root) == "1.0.71", "the hub switched after its trial"
    moved = state.overlay(root)["grok"]
    assert moved["version"] == "1.0.71"
    assert moved["artifacts"]["linux-x64"]["sha256"] == hashlib.sha256(release("1.0.71")).hexdigest()
    assert lina_runners.pins(root=root)["grok"].version == "1.0.71", "the move is laid over the repo pin"
    for node in hub["nodes"]:
        assert [p["runners"]["grok"]["version"] for p in node.sent] == ["1.0.71"]
    record = json.loads(state.record_path(root, state.last(root)["started"]).read_text())
    assert record["runners"]["grok"]["hub"]["state"] == "moved"
    assert {n: r["state"] for n, r in record["runners"]["grok"]["nodes"].items()} == {
        "build-a": "switched", "build-b": "switched"}
    assert "grok hub 1.0.70 -> 1.0.71: moved" in record["events"]
    assert hub["notices"] == [], "a move that went well is in the record only: quiet when fine (lina#531)"
    assert (root / state.HISTORY).read_text().count("\n") == 1, "the previous entry is kept for a way back"


def test_a_failed_trial_keeps_the_current_version_and_holds_until_cleared(hub):
    hub["http"].publish("1.0.71", release("1.0.71", answer="nope"))
    node = FakeNode("build-a")
    hub["nodes"].append(node)

    code, _ = run(hub)

    root = hub["root"]
    assert code == 1
    assert current(root) == "1.0.70"
    assert "grok" not in state.overlay(root)
    assert not (lina_runners.vendor_dir(root, "grok") / "1.0.71").exists(), "the failed version is not kept"
    hold = state.held(root, "grok", "hub")
    assert hold["version"] == "1.0.71" and "did not answer OK" in hold["error"]
    assert node.sent == [], "the nodes do not follow a version the hub could not trial"

    hub["http"].publish("1.0.71", release("1.0.71"))
    hub["http"].downloads.clear()
    run(hub)
    assert current(root) == "1.0.70" and hub["http"].downloads == [], "a held runner is not even downloaded"

    lina_runner_sync.clear("grok", root, out=lambda line: None)
    code, _ = run(hub)
    assert code == 0 and current(root) == "1.0.71", "cleared, it moves again"


def test_a_major_jump_waits_for_a_person_without_a_trial(hub):
    hub["http"].publish("2.0.0", release("2.0.0"))

    run(hub)

    root = hub["root"]
    assert current(root) == "1.0.70" and hub["http"].downloads == []
    assert state.held(root, "grok")["kind"] == "major"
    lina_runner_sync.clear("grok", root, accept="2.0.0", out=lambda line: None)
    run(hub)
    assert current(root) == "2.0.0"


def test_a_source_below_the_pin_is_refused(hub):
    hub["http"].publish("1.0.69", release("1.0.69"))

    run(hub)

    assert current(hub["root"]) == "1.0.70" and "grok" not in state.overlay(hub["root"])
    assert hub["http"].downloads == []
    assert any("went back to 1.0.69" in line for line in hub["notices"][0].splitlines())


def test_a_hash_the_answer_contradicts_moves_nothing(hub):
    hub["http"].publish("1.0.71", release("1.0.71"), {"etag": '"' + "0" * 32 + '"'})
    before = files(hub["root"])

    run(hub)

    root = hub["root"]
    assert current(root) == "1.0.70"
    assert not (lina_runners.vendor_dir(root, "grok") / "1.0.71").exists()
    kept = {k: v for k, v in files(root).items() if not k.startswith(state.SYNC_DIR)}
    assert json.loads(kept.pop("runner-probes.json"))["grok"]["version"] == "1.0.70", \
        "the daily probe of the version it keeps is the only new file (lina#531)"
    assert kept == before


def test_a_busy_node_is_skipped_and_one_that_got_busy_refuses_on_its_side(hub, tmp_path, monkeypatch):
    hub["http"].publish("1.0.71", release("1.0.71"))
    seen_busy = FakeNode("build-a", running=1)
    node_root = tmp_path / "node" / "lina"
    worker_root = node_root / "worker"
    (worker_root / "tasks" / "OT-BUSY").mkdir(parents=True)
    (worker_root / "tasks" / "OT-BUSY" / "request.1").write_text("{}")
    late = FakeNode("build-b")
    late.call = lambda verb, data, timeout: LocalTransport(worker_root).call(verb, data, timeout)
    hub["nodes"].extend([seen_busy, late])

    code, _ = run(hub)

    record = json.loads(state.record_path(hub["root"], state.last(hub["root"])["started"]).read_text())
    results = record["runners"]["grok"]["nodes"]
    assert code == 0
    assert results["build-a"]["state"] == "busy" and seen_busy.sent == []
    assert results["build-b"]["state"] == "busy", "the node checked again right before it installed"
    assert not (node_root / state.OVERLAY).exists(), "a busy node writes no pin"
    assert state.holds(hub["root"])["holds"] == [], "busy is not a failure"
    assert set(state.lag(hub["root"])) == {"build-a/grok", "build-b/grok"}, "they follow on a later day"


def test_a_node_without_the_verb_is_noted_and_a_failed_node_is_held_alone(hub):
    hub["http"].publish("1.0.71", release("1.0.71"))
    old, bad, good = FakeNode("old", verbs=("hello",)), FakeNode("bad", answer="failed"), FakeNode("good")
    hub["nodes"].extend([old, bad, good])

    run(hub)

    root = hub["root"]
    assert old.sent == []
    assert [h["node"] for h in state.holds(root)["holds"]] == ["bad"]
    issues, _ = lina_runner_sync.issue_lines(root)
    assert any(line.startswith("- hold: grok on bad 1.0.71") for line in issues)
    assert not any("on old" in line for line in issues), "a node that needs an update is not an issue"

    bad.sent.clear(), good.sent.clear()
    run(hub)
    assert bad.sent == [] and good.sent == [], "held on one node; the other is already the same"


def test_plan_installs_and_writes_nothing(hub):
    hub["http"].publish("1.0.71", release("1.0.71"))
    node = FakeNode("build-a")
    hub["nodes"].append(node)
    before = files(hub["root"])

    code, lines = run(hub, plan=True)

    assert code == 0
    assert files(hub["root"]) == before
    assert hub["http"].downloads == [] and node.sent == [] and hub["notices"] == []
    assert any("would move" in line for line in lines)
    assert any("would follow" in line for line in lines)


@pytest.mark.parametrize(("hub_is", "hub_state", "nodes"), [
    ("busy", "hub busy", {}),
    ("without a LINA copy", "hub prepare needed", {"build-a": {"state": "same"}}),
    ("free", "would move", {"build-a": {"state": "would follow", "from": "1.0.70", "to": "1.0.71"}}),
], ids=["busy", "no-hub-copy", "normal"])
def test_plan_shows_what_the_run_would_do_on_the_hub_and_writes_nothing(hub, hub_is, hub_state, nodes):
    import shutil
    root = hub["root"]
    hub["http"].publish("1.0.71", release("1.0.71"))
    node = FakeNode("build-a")
    hub["nodes"].append(node)
    if hub_is == "busy":
        _hold_a_hub_slot(root)
    elif hub_is == "without a LINA copy":
        shutil.rmtree(lina_runners.vendor_dir(root, "grok"))
    before = files(root)

    code, lines = run(hub, plan=True, as_json=True)

    grok = json.loads(lines[0])["runners"]["grok"]
    assert code == 0 and grok["hub"]["state"] == hub_state and grok.get("nodes", {}) == nodes
    assert files(root) == before
    assert hub["http"].downloads == [] and node.sent == [] and hub["notices"] == []


def test_a_quiet_day_sends_no_notice(hub):
    hub["http"].publish("1.0.70", release("1.0.70"))
    hub["nodes"].append(FakeNode("build-a"))

    assert run(hub)[0] == 0
    assert hub["notices"] == []


def test_status_issues_prints_only_issue_lines(hub):
    out: list[str] = []
    lina_runner_sync.status(hub["root"], issues_only=True, out=out.append)
    assert out == ["- none"]

    state.add_hold(hub["root"], "grok", "build-a", "1.0.71", "install failed", "node")
    state.update_lag(hub["root"], "build-c", "grok", "1.0.69", "1.0.70", now=0.0)
    out.clear()
    lina_runner_sync.status(hub["root"], issues_only=True, out=out.append)
    assert out[0] == "- hold: grok on build-a 1.0.71 (node): install failed"
    assert out[1].startswith("- lag: grok on build-c 1.0.69, hub 1.0.70 for ")
    assert all(line.startswith("- ") for line in out)


def test_no_secret_reaches_the_output_the_record_or_the_notice(hub, monkeypatch):
    monkeypatch.setenv("CURSOR_API_KEY", SECRET)
    leak = (f"#!{sys.executable}\nimport os, sys\nif '--version' in sys.argv:\n    print('grok 1.0.71')\n"
            f"    sys.exit(0)\nprint('key ' + os.environ['CURSOR_API_KEY'], file=sys.stderr)\nsys.exit(1)\n").encode()
    hub["http"].publish("1.0.71", leak)

    _, lines = run(hub, as_json=True)

    written = b"".join(files(hub["root"]).values()).decode("utf-8", "replace")
    assert SECRET not in "\n".join(lines) and SECRET not in written and SECRET not in "".join(hub["notices"])
    assert "***" in state.held(hub["root"], "grok", "hub")["error"]


def test_a_hub_not_signed_in_is_a_question_not_a_failure(hub):
    hub["http"].publish("1.0.71", release("1.0.71"))
    hub["env"].sign_in = lambda runner: "signed_out"

    code, _ = run(hub)

    root = hub["root"]
    assert code == 0 and current(root) == "1.0.70" and hub["http"].downloads == []
    assert state.holds(root)["holds"] == []
    issues, _ = lina_runner_sync.issue_lines(root)
    assert issues == ["- sign-in: Sign in to grok on 1 node(s): hub"]


def test_the_cli_prints_the_issue_lines(hub, capsys):
    import argparse
    from hermes_cli.subcommands.runners import _sync, build_runners_parser
    parser = argparse.ArgumentParser()
    build_runners_parser(parser.add_subparsers())

    assert _sync(parser.parse_args(["runners", "sync", "status", "--issues"])) == 0
    assert capsys.readouterr().out == "- none\n"


def _hold_a_hub_slot(root: Path) -> Path:
    task = root / "worker" / "tasks" / "OT-HUBWORK"
    task.mkdir(parents=True)
    (task / "request.1").write_text("{}")
    return task


def test_a_busy_hub_moves_nothing_that_day_and_moves_once_free(hub):
    import shutil
    hub["http"].publish("1.0.71", release("1.0.71"))
    node = FakeNode("build-a")
    hub["nodes"].append(node)
    task = _hold_a_hub_slot(hub["root"])

    code, _ = run(hub)

    root = hub["root"]
    record = json.loads(state.record_path(root, state.last(root)["started"]).read_text())
    assert code == 0 and record["runners"]["grok"]["hub"]["state"] == "hub busy"
    assert current(root) == "1.0.70" and "grok" not in state.overlay(root)
    assert hub["http"].downloads == [] and not (lina_runners.vendor_dir(root, "grok") / "1.0.71").exists()
    assert node.sent == [] and state.holds(root)["holds"] == [], "not a hold: tomorrow tries again"

    shutil.rmtree(task)
    assert run(hub)[0] == 0 and current(root) == "1.0.71" and len(node.sent) == 1


def test_a_run_starting_on_the_hub_during_the_trial_stops_the_switch(hub):
    import subprocess
    hub["http"].publish("1.0.71", release("1.0.71"))
    node = FakeNode("build-a")
    hub["nodes"].append(node)

    def run_and_take_a_slot(argv, **kwargs):
        if "-p" in argv:
            _hold_a_hub_slot(hub["root"])
        return subprocess.run(argv, check=False, **{k: v for k, v in kwargs.items() if k != "check"})
    hub["env"].run = run_and_take_a_slot

    run(hub)

    root = hub["root"]
    assert current(root) == "1.0.70" and "grok" not in state.overlay(root)
    assert not (lina_runners.vendor_dir(root, "grok") / "1.0.71").exists() and node.sent == []
    assert state.holds(root)["holds"] == []


def _trial_runner(root: Path):
    runner = lina_runners.RunnerPin(name="claude", installer="binary", version="2.0.0", bin="claude",
                                    sign_in="magpie", entry="claude", trial={"argv": ["-p", "{prompt}"]})
    folder = lina_runners.vendor_dir(root, "claude") / "2.0.0"
    folder.mkdir(parents=True)
    (folder / "claude").write_bytes(release("2.0.0"))
    (folder / "claude").chmod(0o755)
    return runner


def _timing_out_trial(tmp_path, partial):
    import subprocess
    from hermes_cli import lina_runner_sync_trial as trials

    def hang(argv, **kwargs):
        raise subprocess.TimeoutExpired(argv, 300.0, output=partial)
    return trials.trial(tmp_path, _trial_runner(tmp_path), "2.0.0", run=hang)



@pytest.mark.parametrize("partial", [b"OK\n", "OK\n"])
def test_a_trial_that_printed_ok_and_never_exited_says_so(tmp_path, partial):
    assert _timing_out_trial(tmp_path, partial) == (False, "answered OK but did not exit in 300.0s")


@pytest.mark.parametrize("partial", [None, b"", b"warming up\n"])
def test_a_trial_that_printed_nothing_in_time_is_no_answer(tmp_path, partial):
    assert _timing_out_trial(tmp_path, partial) == (False, "no answer in 300.0s")


def test_a_second_run_waits_for_the_first(hub):
    with lina_runner_sync.run_lock(hub["root"]):
        code, lines = run(hub)
    assert code == 1 and lines == ["another `lina runners sync` is running"]


@pytest.mark.parametrize(("reachable", "proxy", "issues", "notes"), [
    (True, {"url": "http://127.0.0.1:3425/v1/models", "health": 503, "models": 0},
     ["- proxy: build-a health 503"], []),
    (True, {"url": "http://127.0.0.1:3425/v1/models", "health": None, "models": 0},
     ["- proxy: build-a health None"], []),
    (True, {"url": "http://127.0.0.1:3425/v1/models", "health": 200, "models": 0},
     ["- proxy: build-a model path lists no model (magpie http://127.0.0.1:3425/v1/models: HTTP 200, 0 models)"], []),
    (True, {"url": "http://127.0.0.1:3425/v1/models", "health": 200, "models": 2}, [], []),
    (True, None, [], []),
    (False, {"url": "http://127.0.0.1:3425/v1/models", "health": 503, "models": 0},
     [], ["- unreachable: build-a"]),
])
def test_daily_issues_report_only_reachable_nodes_with_an_unhealthy_proxy(tmp_path, reachable, proxy, issues, notes):
    """Regression for #614: read the node's proxy fact, preserve missing and unreachable nodes."""
    record = {"nodes": {"build-a": {"reachable": reachable, **({"proxy": proxy} if proxy is not None else {})}}}
    state.write_json(state.record_path(tmp_path, 2_000_000_000), record)

    assert lina_runner_sync.issue_lines(tmp_path, now=2_000_000_001) == (issues, notes)
