"""The runner contract of ``lina worker`` (lina#35), run for every registered runner type through the real hub and
device code over the local transport: omo (a fake omo replaying ``omo host session … --json`` shapes, and a fake
engine under the JS helpers on the real Node) and a ``command`` runner registered from config only (a fake CLI).

Each test drives one task through its lifecycle: start → question → answer → settle → result → follow-up → stop,
and the endings stalled, done_empty and review (``## 다음에 알아야 할 것`` missing). A run is the same
``device.run_task`` the run process executes, started on a thread instead of its own process.
"""

import json
import shutil

import pytest

from hermes_cli.worker import serve
from hermes_cli.worker.hub import Hub
from tests.hermes_cli import _worker_fakes as fakes

NODE = shutil.which("node")


class Lane:
    """One runner type on a worker root with one local device."""

    def __init__(self, kind: str, tmp_path, monkeypatch):
        self.kind, self.tmp = kind, tmp_path
        self.base = tmp_path / "lina"
        self.root = self.base / "worker"
        self.fake = tmp_path / "fake"
        if kind == "omo":
            if NODE is None:
                pytest.skip("the omo helpers run on Node, and no node is on PATH")
            spec = fakes.install_fake_omo(self.fake, NODE)
            monkeypatch.setenv("FAKE_OMO_DIR", str(self.fake))
            monkeypatch.setenv("LINA_WORKER_ANSWER_PAUSE_MS", "0")
            runners = f"omo: {json.dumps(spec)}\n"
        else:
            argv = fakes.install_fake_cli(self.fake)
            monkeypatch.setenv("FAKE_CLI_LOG", str(self.fake / "cli.jsonl"))
            runners = f"fakecli: {json.dumps({'type': 'command', 'argv': argv})}\n"
        self.runners_yaml = runners
        fakes.write_config(self.base, runners)
        self.runner = "omo" if kind == "omo" else "fakecli"
        self.launcher = fakes.ThreadLauncher().install(monkeypatch)
        self.lines: list[str] = []
        self.hub = Hub(self.root, out=self.lines.append)
        serve.serve_once(self.root)

    def stall_grace(self, seconds: float) -> None:
        fakes.write_config(self.base, self.runners_yaml, stall=seconds)

    def scenario(self, first: str, follow_up: str = "done") -> str:
        """The brief text that makes the agent do *first*; for omo also its next turn."""
        (self.fake / "scenario.json").write_text(json.dumps({"first": first, "follow_up": follow_up}))
        return {"question": "ask", "running": "hang"}.get(first, first) if self.kind == "command" else first

    def submit(self, first: str, follow_up: str = "done") -> str:
        token = self.scenario(first, follow_up)
        brief = fakes.brief(self.tmp, f"Write out/ok.txt. SCENARIO:{token}")
        assert self.hub.submit({"briefs": [brief], "mode": "ulw", "topic": "contract", "bot": "sion",
                                "runner": self.runner, "wait": None}) == 0
        task_id = self.lines[0].split()[0]
        serve.serve_once(self.root)
        return task_id

    def job(self, task_id: str) -> dict:
        return self.hub.store.load(task_id)

    def ended(self, task_id: str, run: int) -> str:
        """The ending state the hub recorded for run *run* (the device reported it)."""
        return fakes.until(lambda: next((m.split(":")[0] for m in self.job(task_id)["notified"]
                                         if m.endswith(f":{run}") and not m.startswith("human")), None),
                           what=f"{task_id} run {run} reported")

    def task_dir(self, task_id: str):
        return self.root / "tasks" / task_id


@pytest.fixture(params=["omo", "command"])
def lane(request, tmp_path, monkeypatch):
    built = Lane(request.param, tmp_path, monkeypatch)
    yield built
    built.launcher.join()


def test_start_question_answer_settle_result_follow_up(lane):
    task_id = lane.submit("question")
    fakes.until(lambda: lane.hub.transport(lane.job(task_id)).call(["human", task_id]).json().get("open"),
                what="the run waits on a person")

    assert lane.hub.send(task_id, "blue") == 0
    assert "answered the question of run 1" in lane.lines[-1]
    assert lane.ended(task_id, 1) == "done"

    job = lane.job(task_id)
    out = lane.hub.store.out_dir(job)
    assert job["delivery"] == "ok" and (out / "out" / "ok.txt").read_text() == "ok\n"
    manifest = fakes.as_json(out / "manifest.json")
    assert manifest["next_notes"] == 1 and set(manifest["files"]) >= {"report.md", "out/ok.txt", "final.md"}
    assert (out / "final.md").read_text().strip()
    notices = list(lane.hub.store.notices.glob(f"{task_id}-done-1-*.md"))
    assert len(notices) == 1 and "lina worker accept" in notices[0].read_text()

    lane.scenario("question", follow_up="done")
    assert lane.hub.send(task_id, "SCENARIO:done also add a line") == 0
    assert "started run 2" in lane.lines[-1]
    serve.serve_once(lane.root)
    assert lane.ended(task_id, 2) == "done"
    assert (lane.task_dir(task_id) / "prompt.2").read_text().startswith("ulw ")
    kinds = [r["kind"] for r in lane.hub.store.receipts_of(task_id)]
    assert kinds.count("notified") == 2 and "follow_up" in kinds and "answer" in kinds


def test_stop_ends_a_running_task_as_cancelled(lane):
    task_id = lane.submit("running")
    fakes.until(lambda: (lane.task_dir(task_id) / "handle.json").exists(), what="the runner started")
    fakes.until(lambda: lane.hub.meta(lane.job(task_id)).get("alive"), what="the run is live")
    assert lane.hub.cancel(task_id, "not needed") == 0
    assert lane.ended(task_id, 1) == "cancelled"
    assert (lane.task_dir(task_id) / "state").read_text().strip() == "cancelled"
    assert "not needed" in lane.job(task_id)["notice"]


def test_a_follow_up_into_a_live_run_is_steered_or_refused_by_the_runner(lane):
    task_id = lane.submit("running")
    fakes.until(lambda: (lane.task_dir(task_id) / "handle.json").exists(), what="the runner started")
    if lane.kind == "omo":
        assert lane.hub.send(task_id, "also check the docs") == 0
        assert "took the instruction into run 1" in lane.lines[-1]
        assert "also check the docs" in json.loads((lane.fake / "state.json").read_text())["steers"][0]
    else:
        from hermes_cli.worker.hub import HubError
        with pytest.raises(HubError, match="no channel into a running turn"):
            lane.hub.send(task_id, "also check the docs")
    lane.hub.cancel(task_id, "done with the check")
    assert lane.ended(task_id, 1) == "cancelled"


@pytest.mark.parametrize(("first", "ending"), [("hang", "stalled"), ("empty", "done_empty"), ("nonotes", "review")])
def test_endings(lane, first, ending):
    lane.stall_grace(1.5)
    task_id = lane.submit(first)
    assert lane.ended(task_id, 1) == ending
    notice = lane.job(task_id)["notice"]
    assert {"stalled": "no answer", "done_empty": "nothing delivered", "review": "needs review"}[ending] in notice


def test_the_command_runner_reports_a_failed_cli(tmp_path, monkeypatch):
    lane = Lane("command", tmp_path, monkeypatch)
    task_id = lane.submit("fail")
    assert lane.ended(task_id, 1) == "failed"
    assert "environment" in lane.job(task_id)["notice"]
    lane.launcher.join()
