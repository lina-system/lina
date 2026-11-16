"""Result records of a run (lina#527): a run that ends with files in ``out/`` gets one draft record per file, found
from its to-do, its goal and its Desk path; only the owner's ``artifact final`` makes a draft final.

The worker is the real hub with no work device, as in ``test_work_run``: the test ends a run by writing its job file
and the fetched ``out/`` the hub would hold, then runs ``run-sync``."""

from __future__ import annotations

import json

import pytest

from hermes_cli.work import ledger
from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.brief = tmp_path / "brief.md"
    env.brief.write_text("Write out/plan.md and out/data/table.csv\n", encoding="utf-8")
    env.goal_and_todos(todos=2)
    return env


def _end(work: WorkEnv, todo: str, files: dict[str, str], state: str = "done") -> str:
    """Hand *todo* to a run, then end it with *files* fetched into the hub's ``out/<agent>/<run>/out``."""
    work.ok("run", "submit", todo, "--brief", str(work.brief))
    jobs = work.root / "worker" / "hub" / "jobs"
    run_id = max(p.stem for p in jobs.glob("*.json"))
    _deliver(work, run_id, files, state)
    return run_id


def _deliver(work: WorkEnv, run_id: str, files: dict[str, str], state: str = "done") -> None:
    path = work.root / "worker" / "hub" / "jobs" / f"{run_id}.json"
    job = json.loads(path.read_text(encoding="utf-8"))
    job.update(state=state, delivery="ok", host="here")
    path.write_text(json.dumps(job), encoding="utf-8")
    for name, text in files.items():
        target = work.root / "worker" / "hub" / "out" / job["bot"] / run_id / "out" / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text, encoding="utf-8")
    work.ok("run-sync")


def _artifacts(work: WorkEnv, *args: str):
    return json.loads(work.ok("artifact", *args, "--json"))


def _rows(sql: str, params=()):
    db = ledger.open_db()
    try:
        return db.execute(sql, params).fetchall()
    finally:
        db.close()


def test_a_finished_run_with_two_files_makes_two_draft_records_and_a_run_with_none_makes_none(work):
    run_id = _end(work, "T1", {"plan.md": "# plan\n", "data/table.csv": "a,b\n"})
    empty = _end(work, "T2", {}, state="done_empty")

    found = _artifacts(work, "list", run_id)
    assert [(a["path"], a["kind"], a["task"], a["goal"], a["repo"], a["commit"]) for a in found] == [
        (f"Areas/Dev/G1/{run_id}/plan.md", "draft", "T1", "G1", None, None),
        (f"Areas/Dev/G1/{run_id}/data/table.csv", "draft", "T1", "G1", None, None)]
    assert (work.desk / f"Areas/Dev/G1/{run_id}/plan.md").read_text(encoding="utf-8") == "# plan\n"
    assert _artifacts(work, "list", empty) == []
    work.ok("run-sync")
    assert _rows("SELECT count(*) FROM artifact") == [(2,)]


def test_a_result_is_found_from_its_todo_its_goal_and_its_desk_path(work):
    run_id = _end(work, "T1", {"plan.md": "# plan\n", "data/table.csv": "a,b\n"})
    path = f"Areas/Dev/G1/{run_id}/plan.md"

    assert [a["path"] for a in _artifacts(work, "list", "t1")] == [path, f"Areas/Dev/G1/{run_id}/data/table.csv"]
    assert len(_artifacts(work, "list", "G1")) == 2
    assert _artifacts(work, "list", "T2") == []
    one = _artifacts(work, "find", "desk:" + path)
    assert ([a["path"] for a in one["artifacts"]], one["runs"], one["tasks"], one["goals"]) == (
        [path], [run_id], ["T1"], ["G1"])
    folder = _artifacts(work, "find", "Areas/Dev/G1/")
    assert (len(folder["artifacts"]), folder["runs"], folder["tasks"], folder["goals"]) == (2, [run_id], ["T1"], ["G1"])
    assert _artifacts(work, "find", "Areas/Dev/G1/OT")["artifacts"] == []


def test_only_the_owners_final_makes_a_draft_final_and_a_final_file_is_never_overwritten(work):
    run_id = _end(work, "T1", {"plan.md": "first\n"})
    desk_file = work.desk / f"Areas/Dev/G1/{run_id}/plan.md"
    _deliver(work, run_id, {"plan.md": "second\n"})
    (draft,) = _artifacts(work, "list", "T1")
    assert (draft["kind"], desk_file.read_text(encoding="utf-8")) == ("draft", "second\n")

    assert f"#{draft['id']}" in work.ok("artifact", "final", str(draft["id"]))
    _deliver(work, run_id, {"plan.md": "third\n"})
    (final,) = _artifacts(work, "list", "T1")
    assert (final["id"], final["kind"], desk_file.read_text(encoding="utf-8")) == (draft["id"], "final", "second\n")
    assert _rows("SELECT actor_id, data FROM event WHERE run_id = ? AND kind = 'status'", (run_id,)) == [
        ("owner", json.dumps({"op": "artifact_final", "artifact": draft["id"]}))]
    code, _out, err = work.run("artifact", "final", str(draft["id"] + 100))
    assert code != 0 and f"#{draft['id'] + 100}" in err

