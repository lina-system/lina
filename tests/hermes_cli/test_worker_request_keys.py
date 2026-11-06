"""Request keys on ``submit`` and ``send`` (lina#219): a call retried after its reply was lost is the same request.
The same key with the same arguments returns the first result and starts nothing, the same key with other arguments
is refused, the hub's key records survive a hub restart, and the device keeps the keys of ``follow_up``, ``answer``
and ``send``."""

import json
import re
from types import SimpleNamespace

import pytest

from hermes_cli.worker import device, hub as hub_mod, protocol, serve
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.transport import Reply
from tests.hermes_cli import _worker_fakes as fakes
from tests.hermes_cli.test_worker_hermes_card import _cli


class HubStopped(BaseException):
    """The hub process ends here (no handler of the hub catches it)."""


@pytest.fixture
def lane(tmp_path, monkeypatch):
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    monkeypatch.setenv("LINA_HOME", str(base))
    fakes.write_config(base, f"fakecli: {json.dumps({'type': 'command', 'argv': argv, 'access': 'workspace-write'})}\n")
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
    with pytest.raises(HubError, match="follow-up failed"):
        lane.hub.send(task_id, "more", request_key="rk-lost")
    assert lane.hub.send(task_id, "more", request_key="rk-lost") == 0
    assert [(r["action"], r["run"]) for r in replies] == [("requested", 2), ("requested", 2)]
    assert replies[1].get("replayed") is True
    assert _requests(lane, task_id) == ["request.1", "request.2"]
    assert lane.hub.store.load(task_id)["runs"] == 2


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
