"""Request keys on ``submit`` and ``send`` (lina#219): a call retried after its reply was lost is the same request.
The same key with the same arguments returns the first result and starts nothing, the same key with other arguments
is refused, the hub's key records survive a hub restart, and the device keeps the keys of ``follow_up``, ``answer``
and ``send``."""

import json
import os
import re
from types import SimpleNamespace

import pytest

from hermes_cli.worker import device, hub as hub_mod, kanban_link, protocol, serve
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.request_keys import new_key
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes
from tests.hermes_cli.test_worker_hermes_card import _cli


class HubStopped(BaseException):
    """The hub process ends here (no handler of the hub catches it)."""


@pytest.fixture
def lane(tmp_path, monkeypatch):
    # submit and send read the card from this process and its ancestors: a test run inside a card session has one.
    for name in [name for name in os.environ if name.startswith("HERMES_KANBAN_")]:
        monkeypatch.delenv(name)
    monkeypatch.setattr(kanban_link, "card_from_ancestors", lambda *a, **k: None)
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    monkeypatch.setenv("LINA_HOME", str(base))
    spec = {"type": "command", "argv": argv, "access_args": {mode: [] for mode in hub_mod.ACCESS}}
    fakes.write_config(base, f"fakecli: {json.dumps(spec)}\n")
    launched: list[tuple[str, int]] = []
    monkeypatch.setattr(device, "launch", lambda root, task, run: launched.append((task.id, run)))
    root = base / "worker"
    serve.serve_once(root)
    lines: list[str] = []
    return SimpleNamespace(root=root, hub=Hub(root, out=lines.append), lines=lines, launched=launched, tmp=tmp_path,
                           mp=monkeypatch)


def _opts(lane, key: str = "rk-one", **changes) -> dict:
    return {"briefs": [fakes.brief(lane.tmp, "SCENARIO:done one")], "mode": "ulw", "topic": "keys", "bot": "sion",
            "runner": "fakecli", "wait": None, "request_key": key, **changes}


def _submitted(lane) -> str:
    assert lane.hub.submit(_opts(lane)) == 0
    (job,) = lane.hub.store.all()
    return job["id"]


def _ended(lane, task_id: str) -> None:
    """Run 1 was taken by serve and ended on the device."""
    serve.serve_once(lane.root)
    (lane.root / "tasks" / task_id / "rc.1").write_text("0\n")


def _kinds(hub: Hub, task_id: str) -> list[str]:
    return [record["kind"] for record in hub.store.receipts_of(task_id)]


def _requests(lane, task_id: str) -> list[str]:
    return sorted(p.name for p in (lane.root / "tasks" / task_id).glob("request.*"))


def test_a_submit_retried_after_a_lost_reply_is_the_same_task(lane):
    task_id = _submitted(lane)
    lane.lines.clear()
    assert lane.hub.submit(_opts(lane)) == 0
    assert [job["id"] for job in lane.hub.store.all()] == [task_id]
    assert lane.lines == [f"{task_id} replay of request key rk-one"]
    kinds = _kinds(lane.hub, task_id)
    assert (kinds.count("placed"), kinds.count("run_started")) == (1, 1)
    replayed = [r for r in lane.hub.store.receipts_of(task_id) if r["kind"] == "request_replayed"]
    assert [(r["key"], r["op"], bool(r["first_at"])) for r in replayed] == [("rk-one", "submit", True)]
    serve.serve_once(lane.root)
    assert lane.launched == [(task_id, 1)]
    assert lane.hub.store.load(task_id)["request_key"] == "rk-one"


def test_a_send_retried_after_a_lost_reply_starts_one_run(lane):
    task_id = _submitted(lane)
    _ended(lane, task_id)
    assert lane.hub.send(task_id, "SCENARIO:done more", request_key="rk-send") == 0
    lane.lines.clear()
    assert lane.hub.send(task_id, "SCENARIO:done more", request_key="rk-send") == 0
    assert lane.lines == [f"{task_id} replay of request key rk-send: requested run 2"]
    assert _requests(lane, task_id) == ["request.1", "request.2"]
    job = lane.hub.store.load(task_id)
    assert job["runs"] == 2 and job["request_keys"]["rk-send"]["run"] == 2
    kinds = _kinds(lane.hub, task_id)
    assert kinds.count("follow_up") == 1 and kinds.count("request_replayed") == 1


def test_a_follow_up_whose_device_reply_was_lost_is_not_run_twice(lane):
    task_id = _submitted(lane)
    _ended(lane, task_id)
    replies: list[dict] = []
    real = hub_mod.for_device

    def lossy(dev, root):
        transport = real(dev, root)
        call = transport.call

        def dropped(verb, stdin=b"", timeout=120):
            reply = call(verb, stdin, timeout)
            if verb[0] == "follow_up":
                replies.append(reply.json())
                if len(replies) == 1:
                    return Reply(255, b"", "connection reset by peer")
            return reply

        transport.call = dropped
        return transport

    lane.mp.setattr(hub_mod, "for_device", lossy)
    with pytest.raises(HubError, match="follow_up got no reply .* send it again with request key rk-lost"):
        lane.hub.send(task_id, "more", request_key="rk-lost")
    assert lane.hub.send(task_id, "more", request_key="rk-lost") == 0
    assert [(r["action"], r["run"]) for r in replies] == [("requested", 2), ("requested", 2)]
    assert replies[1].get("replayed") is True
    assert _requests(lane, task_id) == ["request.1", "request.2"]
    assert lane.hub.store.load(task_id)["runs"] == 2


def test_a_replayed_follow_up_never_moves_the_task_back_to_its_run(lane):
    """K1 started run 2 and its reply was lost; K2 started run 3 with less access. The retry of K1 gets run 2's reply
    back: the task stays on run 3 with K2's access, and run 3's end is its own notice."""
    task_id = _submitted(lane)
    _ended(lane, task_id)
    real, lost = hub_mod.for_device, []

    def lossy(dev, root):
        transport = real(dev, root)
        call = transport.call

        def dropped(verb, stdin=b"", timeout=120):
            reply = call(verb, stdin, timeout)
            if verb[0] == "follow_up" and json.loads(stdin)["request_key"].split("#")[0] == "rk-k1" and not lost:
                lost.append(reply.json())
                return Reply(255, b"", "connection reset by peer")
            return reply

        transport.call = dropped
        return transport

    lane.mp.setattr(hub_mod, "for_device", lossy)
    with pytest.raises(HubError):
        lane.hub.send(task_id, "more", request_key="rk-k1")
    assert [(r["action"], r["run"]) for r in lost] == [("requested", 2)]
    serve.serve_once(lane.root)
    (lane.root / "tasks" / task_id / "rc.2").write_text("0\n")
    assert lane.hub.send(task_id, "less", access="read-only", request_key="rk-k2") == 0
    job = lane.hub.store.load(task_id)
    assert (job["runs"], job["access"], job["grant"]["access"]) == (3, "read-only", "read-only")
    lane.lines.clear()
    assert lane.hub.send(task_id, "more", request_key="rk-k1") == 0
    assert lane.lines == [f"{task_id} replay of request key rk-k1: requested run 2"]
    job = lane.hub.store.load(task_id)
    assert (job["runs"], job["state"], job["access"], job["grant"]["access"]) == (3, "running", "read-only",
                                                                                 "read-only")
    assert job["request_keys"]["rk-k1"]["run"] == 2
    assert _kinds(lane.hub, task_id).count("request_replayed") == 1
    assert _requests(lane, task_id) == ["request.1", "request.2", "request.3"]
    assert lane.hub.notify(task_id, "done", "3") != "earlier run"
    assert lane.hub.notify(task_id, "done", "3") == "already"
    ends = [r for r in lane.hub.store.receipts_of(task_id) if r["kind"] == "notified"]
    assert [(r["state"], r["run"]) for r in ends] == [("done", "3")]


def test_an_answer_whose_reply_was_lost_is_not_applied_again_by_another_verb(lane):
    """The device took the answer, the reply was lost and the question closed: the retry must not steer or start a
    run with the same words; it returns the first answer."""
    task_id = _submitted(lane)
    serve.serve_once(lane.root)
    task_dir = lane.root / "tasks" / task_id
    (task_dir / "live.1.json").write_text(json.dumps({"state": "waiting_human"}), encoding="utf-8")
    lane.mp.setattr(device, "alive", lambda task, run: True)
    lane.mp.setattr(hub_mod, "ANSWER_CLOSE_WAIT_S", 0)
    answers, steers = [], []

    def answer(worker_root, tid, text):
        answers.append(text)
        (task_dir / "live.1.json").write_text(json.dumps({"state": "running"}), encoding="utf-8")
        return {"action": "answer", "steps": 1, "still_pending": 0, "run": 1}

    lane.mp.setattr(device, "answer", answer)
    lane.mp.setattr(device, "steer", lambda worker_root, tid, text: steers.append(text) or {"action": "steered",
                                                                                            "run": 1})
    real, lost = hub_mod.for_device, []

    def lossy(dev, root):
        transport = real(dev, root)
        call = transport.call

        def dropped(verb, stdin=b"", timeout=120):
            reply = call(verb, stdin, timeout)
            if verb[0] == "answer" and not lost:
                lost.append(reply)
                return Reply(255, b"", "connection reset by peer")
            return reply

        transport.call = dropped
        return transport

    lane.mp.setattr(hub_mod, "for_device", lossy)
    with pytest.raises(HubError):
        lane.hub.send(task_id, "blue", request_key="rk-answer")
    files = {p.name: p.read_bytes() for p in task_dir.iterdir()
             if p.name.startswith(("prompt.", "request.", "steer."))}
    lane.lines.clear()
    assert lane.hub.send(task_id, "blue", request_key="rk-answer") == 0
    assert (answers, steers) == (["blue"], [])
    assert lane.lines == [f"{task_id} replay of request key rk-answer: answer run 1"]
    assert {p.name: p.read_bytes() for p in task_dir.iterdir()
            if p.name.startswith(("prompt.", "request.", "steer."))} == files
    assert lane.hub.store.load(task_id)["request_keys"]["rk-answer"]["action"] == "answer"


def test_a_caller_key_never_meets_the_key_of_a_next_run_after_an_answer(lane, capsys):
    """An answer with key k left its question open and the run closed: the hub asked for the next run under a device
    key of its own. A caller key that looks like it is refused and the device is as it was."""
    task_id = _submitted(lane)
    serve.serve_once(lane.root)
    task_dir = lane.root / "tasks" / task_id
    (task_dir / "live.1.json").write_text(json.dumps({"state": "waiting_human"}), encoding="utf-8")
    closed: list[str] = []
    lane.mp.setattr(device, "alive", lambda task, run: not closed)
    lane.mp.setattr(hub_mod, "ANSWER_CLOSE_WAIT_S", 0)

    def answer(worker_root, tid, text):
        closed.append(text)
        (task_dir / "live.1.json").write_text(json.dumps({"state": "running"}), encoding="utf-8")
        (task_dir / "rc.1").write_text("0\n")
        return {"action": "answer", "steps": 0, "still_pending": 1, "run": 1}

    lane.mp.setattr(device, "answer", answer)
    assert lane.hub.send(task_id, "blue", request_key="k") == 0
    assert (closed, lane.hub.store.load(task_id)["runs"]) == (["blue"], 2)
    state = {p.name: p.read_bytes() for p in task_dir.iterdir() if p.is_file()}
    capsys.readouterr()
    assert _cli(["send", task_id, "green", "--key", "k#next"]) == 2
    assert capsys.readouterr().err == (
        "lina worker: request key must match ^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$: k#next\n")
    assert {p.name: p.read_bytes() for p in task_dir.iterdir() if p.is_file()} == state
    assert lane.hub.store.load_key("k#next") is None


@pytest.mark.parametrize("key", ["a b", "a/b", "k#req", "-lead", "x" * 129, "k\n"])
def test_a_key_outside_the_caller_pattern_is_refused_before_anything_is_written(lane, key):
    with pytest.raises(HubError, match=r"^request key must match .+: "):
        lane.hub.submit(_opts(lane, key=key))
    assert lane.hub.store.all() == []
    task_id = _submitted(lane)
    _ended(lane, task_id)
    receipts = lane.hub.store.receipts_of(task_id)
    with pytest.raises(HubError, match=r"^request key must match .+: "):
        lane.hub.send(task_id, "more", request_key=key)
    assert lane.hub.store.receipts_of(task_id) == receipts
    assert [p.name for p in lane.hub.store.keys.glob("*.json")] == [f"{lane.hub.store.key_name('rk-one')}.json"]
    assert _requests(lane, task_id) == ["request.1"]


def test_the_keys_the_repo_makes_fit_the_caller_pattern(lane):
    task_id = _submitted(lane)
    _ended(lane, task_id)
    assert lane.hub.send(task_id, "more", request_key="lina-work:12") == 0
    assert lane.hub.store.load(task_id)["request_keys"]["lina-work:12"]["run"] == 2
    assert lane.hub.submit(_opts(lane, key="lina-schedule:OS-SIO0000000000:1700000000")) == 0
    assert lane.hub.submit(_opts(lane, key=new_key())) == 0


def test_the_same_key_with_other_arguments_is_refused(lane):
    task_id = _submitted(lane)
    _ended(lane, task_id)
    assert lane.hub.send(task_id, "first message", request_key="rk-send") == 0
    jobs, receipts = lane.hub.store.all(), lane.hub.store.receipts_of(task_id)
    with pytest.raises(HubError, match=rf"^request key rk-one was already used for submit {task_id} at .+ with "
                                       "other arguments$"):
        lane.hub.submit(_opts(lane, topic="other"))
    with pytest.raises(HubError, match=rf"^request key rk-send was already used for send {task_id} at .+ with "
                                       "other arguments$"):
        lane.hub.send(task_id, "another message", request_key="rk-send")
    assert lane.hub.store.all() == jobs
    assert lane.hub.store.receipts_of(task_id) == receipts
    assert _requests(lane, task_id) == ["request.1", "request.2"]


def test_a_hub_restart_between_the_two_calls_replays(lane):
    task_id = _submitted(lane)
    _ended(lane, task_id)
    assert lane.hub.send(task_id, "more", request_key="rk-send") == 0
    lines: list[str] = []
    restarted = Hub(lane.root, out=lines.append)
    assert restarted.store is not lane.hub.store
    assert restarted.submit(_opts(lane)) == 0
    assert restarted.send(task_id, "more", request_key="rk-send") == 0
    assert lines == [f"{task_id} replay of request key rk-one", f"{task_id} replay of request key rk-send: requested run 2"]
    assert [job["id"] for job in restarted.store.all()] == [task_id]
    assert _requests(lane, task_id) == ["request.1", "request.2"]


def test_a_hub_that_stopped_after_writing_the_key_returns_the_task_and_never_bids_again(lane):
    real_put = hub_mod.put

    def put(transport, task_id, name, data):
        real_put(transport, task_id, name, data)
        if name == "request.1":
            raise HubStopped(name)

    with pytest.MonkeyPatch.context() as stopping:
        stopping.setattr(hub_mod, "put", put)
        with pytest.raises(HubStopped):
            lane.hub.submit(_opts(lane))
    (job,) = lane.hub.store.all()
    assert job["state"] == "placing"

    def bid(*args, **kwargs):
        raise AssertionError("the task was bid out again")

    lane.mp.setattr(hub_mod.bidding, "bid", bid)
    lines: list[str] = []
    assert Hub(lane.root, out=lines.append).submit(_opts(lane)) == 0
    assert lines == [f"{job['id']} replay of request key rk-one"]
    assert [j["id"] for j in lane.hub.store.all()] == [job["id"]]
    assert _kinds(lane.hub, job["id"]).count("placed") == 1


def test_a_device_without_request_keys_still_works_and_is_noted_once(lane):
    real = protocol.hello
    lane.mp.setattr(protocol, "hello", lambda root: {**real(root), "capabilities": {
        k: v for k, v in real(root)["capabilities"].items() if k != "request_keys"}})
    task_id = _submitted(lane)
    _ended(lane, task_id)
    assert lane.hub.send(task_id, "more", request_key="rk-a") == 0
    (lane.root / "tasks" / task_id / "rc.2").write_text("0\n")
    assert lane.hub.send(task_id, "again", request_key="rk-b") == 0
    assert lane.hub.send(task_id, "again", request_key="rk-b") == 0
    assert _requests(lane, task_id) == ["request.1", "request.2", "request.3"]
    assert _kinds(lane.hub, task_id).count("device_without_keys") == 1


def _device_task(tmp_path) -> tuple:
    root = tmp_path / "lina" / "worker"
    task_id = "OT-KEY0101000000"
    task_dir = root / "tasks" / task_id
    task_dir.mkdir(parents=True)
    (task_dir / "job.json").write_text(json.dumps({"id": task_id, "access": "workspace-write"}), encoding="utf-8")
    for name, text in (("prompt.1", "first"), ("request.1", "{}"), ("taken.1", "0"), ("rc.1", "0\n")):
        (task_dir / name).write_text(text, encoding="utf-8")
    return root, task_id, task_dir


def test_the_device_follow_up_with_one_key_changes_the_task_once(tmp_path):
    root, task_id, task_dir = _device_task(tmp_path)
    payload = json.dumps({"prompt": "next", "message": "next", "access": "workspace-write",
                          "request_key": "rk-device"}).encode("utf-8")
    replies = [json.loads(device.dispatch(root, ["follow_up", task_id], payload)[1]) for _ in range(2)]
    assert [(r["action"], r["run"]) for r in replies] == [("requested", 2), ("requested", 2)]
    assert replies[1]["replayed"] is True
    assert sorted(p.name for p in task_dir.glob("request.*")) == ["request.1", "request.2"]
    assert sorted(p.name for p in task_dir.glob("prompt.*")) == ["prompt.1", "prompt.2"]


@pytest.mark.parametrize("verb, handler", [("answer", "answer"), ("send", "steer")])
def test_the_device_answer_and_send_with_one_key_act_once(tmp_path, monkeypatch, verb, handler):
    root, task_id, _ = _device_task(tmp_path)
    acted: list[str] = []
    monkeypatch.setattr(device, handler, lambda worker_root, tid, text: acted.append(text) or {
        "action": "answer" if verb == "answer" else "steered", "run": 1})
    replies = [json.loads(device.dispatch(root, [verb, task_id, "rk-device"], b"blue")[1]) for _ in range(2)]
    assert acted == ["blue"]
    assert replies[0]["action"] == replies[1]["action"] and replies[1]["replayed"] is True
    device.dispatch(root, [verb, task_id, "rk-other"], b"green")
    assert acted == ["blue", "green"]


def test_a_key_the_device_has_seen_returns_its_reply_whatever_the_verb(tmp_path, monkeypatch):
    root, task_id, task_dir = _device_task(tmp_path)
    acted: list[str] = []
    monkeypatch.setattr(device, "answer", lambda worker_root, tid, text: acted.append(f"answer {text}") or {
        "action": "answer", "steps": 1, "still_pending": 0, "run": 1})
    monkeypatch.setattr(device, "steer", lambda worker_root, tid, text: acted.append(f"steer {text}") or {
        "action": "steered", "run": 1})
    first = json.loads(device.dispatch(root, ["answer", task_id, "rk-device"], b"blue")[1])
    steered = json.loads(device.dispatch(root, ["send", task_id, "rk-device"], b"blue")[1])
    payload = json.dumps({"prompt": "blue", "message": "blue", "access": "workspace-write", "request_key": "rk-device"})
    followed = json.loads(device.dispatch(root, ["follow_up", task_id], payload.encode("utf-8"))[1])
    assert acted == ["answer blue"]
    assert steered == followed == {**first, "replayed": True}
    assert sorted(p.name for p in task_dir.glob("request.*")) == ["request.1"]


def test_the_cli_makes_a_key_when_none_is_given_and_takes_one_with_key(lane, capsys):
    brief = fakes.brief(lane.tmp, "SCENARIO:done cli")
    submit = ["submit", "--brief", brief, "--topic", "cli", "--bot", "sion", "--runner", "fakecli"]
    assert _cli(submit) == 0
    said = capsys.readouterr()
    assert re.fullmatch(r"request key: rk-[0-9a-f]{32}", said.err.splitlines()[0])
    assert re.match(r"OT-\S+ handed out ", said.out)  # callers still read the task ID first on stdout
    assert _cli([*submit, "--key", "rk-cli"]) == 0
    assert "request key:" not in capsys.readouterr().err
    (task_id,) = [job["id"] for job in lane.hub.store.all() if job.get("request_key") == "rk-cli"]
    assert _cli([*submit, "--key", "rk-cli"]) == 0
    assert capsys.readouterr().out.splitlines() == [f"{task_id} replay of request key rk-cli"]
    _ended(lane, task_id)
    assert _cli(["send", task_id, "more", "--key", "rk-cli-send"]) == 0
    capsys.readouterr()
    assert _cli(["send", task_id, "more", "--key", "rk-cli-send"]) == 0
    assert capsys.readouterr().out.splitlines() == [f"{task_id} replay of request key rk-cli-send: requested run 2"]
    assert _requests(lane, task_id) == ["request.1", "request.2"]
