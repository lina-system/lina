"""``lina work next`` (lina#63): on a recorded day of lina-system/lina its picks match today's picker script (the
prototype's ``decide`` was run on the same fixture and its answers are stored in it); a red dev never holds a start;
the current sprint is the owner's setting; ``--submit`` hands a pick to its runner through ``lina worker`` and,
inside a work card, the run is recorded on that card."""
from __future__ import annotations

import json
import textwrap
from pathlib import Path

import pytest

from hermes_cli.delivery import next as picker
from hermes_cli.delivery import settings
from tests.hermes_cli._work_fakes import FakeRecord, day_gh, recorded_day

REPO = "lina-system/lina"


def _config(base: Path, extra: str = "") -> None:
    base.mkdir(parents=True, exist_ok=True)
    (base / "config.yaml").write_text(textwrap.dedent("""\
        worker:
          runners:
            dsh: {type: command, argv: [dsh]}
            grok-cursor: {type: command, argv: [grok]}
            agy: {type: command, argv: [agy]}
        """) + extra, encoding="utf-8")


def _plan(base: Path, jobs=None) -> dict:
    return picker.decide(picker.collect(day_gh(recorded_day()), [REPO], FakeRecord(jobs), base))


def _compare(plan: dict, expected: dict) -> None:
    opened = [d["issue"] for d in plan["decisions"] if d["action"] == "open"]
    assert opened == expected["open"]
    assert {str(d["issue"]): d["runner"] for d in plan["decisions"] if d["action"] == "open"} == expected["runner"]
    assert {str(d["issue"]): d["kind"] for d in plan["decisions"] if d["action"] == "skip"} == expected["hold"]


@pytest.mark.parametrize("sprint", ["S1", "S2", "S3", "S4", "S5"])
def test_picks_match_the_prototype_on_the_recorded_day(tmp_path, sprint):
    _config(tmp_path)
    settings.set_sprint(sprint, tmp_path)
    _compare(_plan(tmp_path), recorded_day()["prototype"]["by_sprint"][sprint])


def test_a_running_job_holds_its_issue_as_the_prototype_does(tmp_path):
    case = recorded_day()["prototype"]["with_running_job"]
    _config(tmp_path, f"work:\n  sprint: {case['sprint']}\n")
    jobs = [{"id": job["id"], "state": job["state"], "text": f"{REPO}#{job['issue']}"} for job in case["jobs"]]
    plan = _plan(tmp_path, jobs)
    _compare(plan, case)
    assert "hold lina-system/lina#102 (already running: job OT-RUN1)" in picker.lines_of(plan)


def _snapshot(red=None) -> dict:
    issues = [{"n": 1, "title": "feat(store): one", "body": "Sprint: M0 S3 — x", "labels": ["omo"], "parent": False},
              {"n": 2, "title": "feat(store): two", "body": "Sprint: M0 S3", "labels": ["omo"], "parent": False},
              {"n": 3, "title": "feat(ui): three", "body": "Sprint: M0 S3", "labels": ["omo"], "parent": False},
              {"n": 4, "title": "feat(ui): four", "body": "Sprint: M0 S3", "labels": ["omo"], "parent": False}]
    repo = {"issues": issues, "blocked_by": {4: [{"repo": REPO, "n": 3, "state": "open"}]},
            "pulls": [{"n": 9, "head": "chore/x", "body": ""}] * 30, "branches": [], "dev_red": red, "error": None}
    return {"sprint": ("M0", 3), "repos": {REPO: repo}, "jobs": [{"id": f"OT-{i}", "state": "running",
            "text": "elsewhere"} for i in range(20)], "runners": {"omo": "omo"}, "scopes": ["store"]}


def test_only_an_open_dependency_or_the_same_scope_holds_a_start_and_red_dev_holds_none():
    plan = picker.decide(_snapshot(red=["Python tests (Linux)"]))
    assert [d["issue"] for d in plan["decisions"] if d["action"] == "open"] == [3, 1]
    holds = {d["issue"]: d["kind"] for d in plan["decisions"] if d["action"] == "skip"}
    assert holds == {2: "scope", 4: "blocked"}
    lines = picker.lines_of(plan)
    assert lines[0] == "dev red in lina-system/lina: merges wait, starts do not (Python tests (Linux))"
    assert "hold lina-system/lina#2 (same contract running: store #1 opened now)" in lines


def test_the_sprint_is_the_owners_setting_and_never_moves_by_itself(tmp_path):
    _config(tmp_path)
    plan = _plan(tmp_path)
    assert settings.sprint(tmp_path) is None
    assert not [d for d in plan["decisions"] if d["action"] == "open"]
    assert picker.lines_of(plan)[0] == "no current sprint: set it with `lina work sprint current <S>`"
    assert settings.set_sprint("m0 s2", tmp_path) == "M0 S2"
    _plan(tmp_path)
    assert settings.sprint(tmp_path) == ("M0", 2)
    with pytest.raises(ValueError):
        settings.set_sprint("next", tmp_path)


def test_submit_hands_a_pick_to_its_runner_and_records_it_on_the_work_card(tmp_path, monkeypatch):
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    from hermes_cli.worker import serve
    from hermes_cli.worker.hub_store import Store
    from tests.hermes_cli import _worker_fakes as fakes

    home = tmp_path / "hermes_home"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for var in ("HERMES_KANBAN_DB", "HERMES_KANBAN_WORKSPACES_ROOT", "HERMES_KANBAN_HOME", "HERMES_KANBAN_BOARD"):
        monkeypatch.delenv(var, raising=False)
    kb._INITIALIZED_PATHS.clear()
    with kbc.connect_closing() as conn:
        card = kb.create_task(conn, title="work card: the next issues")
    monkeypatch.setenv("HERMES_KANBAN_TASK", card)
    base, fake = tmp_path / "lina", tmp_path / "fake"
    argv = fakes.install_fake_cli(fake)
    monkeypatch.setenv("FAKE_CLI_LOG", str(fake / "cli.jsonl"))
    fakes.write_config(base, f"dsh: {json.dumps({'type': 'command', 'argv': argv})}\n")
    launcher = fakes.ThreadLauncher().install(monkeypatch)
    serve.serve_once(base / "worker")
    plan = {"decisions": [{"action": "open", "repo": REPO, "issue": 12, "runner": "dsh", "waiting": 0}]}

    assert picker.submit(plan, base)
    launcher.join()

    job = Store(base / "worker").all()[0]
    assert job["runner"] == "dsh" and job["topic"] == f"{REPO}#12" and job["repo"] == REPO
    assert f"Do {REPO} issue #12" in Path(job["brief"]).read_text(encoding="utf-8")
    with kbc.connect_closing() as conn:
        started = [e for e in kb.list_events(conn, card) if e.kind == "worker_run"]
    assert [(e.payload["run_id"], e.payload["runner"]) for e in started] == [(job["id"], "dsh")]
    assert plan["decisions"][0]["submit"] == {"ok": True, "id": job["id"]}


def _issue(n, title, body="", labels=("omo",)):
    return {"n": n, "title": title, "body": "Sprint: M0 S3\n" + body, "labels": list(labels), "parent": False}


def _two(issues, blocked=None, pulls=(), jobs=()):
    repo = {"issues": issues, "blocked_by": blocked or {}, "pulls": list(pulls), "branches": [], "dev_red": None,
            "error": None}
    return {"sprint": ("M0", 3), "repos": {REPO: repo}, "jobs": list(jobs), "runners": {"omo": "omo"},
            "scopes": ["store"]}


def _kinds(plan):
    return {d["issue"]: (d["kind"], d["reason"]) for d in plan["decisions"]}


def test_serial_with_holds_both_ways_when_the_naming_issue_opens_first():
    # #1 names #2 and ranks first (#9 waits behind it): #2 must not open in the same pass.
    issues = [_issue(1, "feat(ui): one", "Serial with: #2"), _issue(2, "feat(ui): two"), _issue(9, "feat(ui): nine")]
    plan = picker.decide(_two(issues, blocked={9: [{"repo": REPO, "n": 1, "state": "open"}]}))
    kinds = _kinds(plan)
    assert kinds[1][0] == "open"
    assert kinds[2] == ("scope", "same contract running: Serial with #1 opened now")


def test_serial_with_holds_the_named_issue_while_the_naming_one_runs():
    issues = [_issue(1, "feat(ui): one", "Serial with: #2"), _issue(2, "feat(ui): two")]
    plan = picker.decide(_two(issues, jobs=[{"id": "OT-1", "state": "running", "text": f"{REPO}#1"}]))
    kinds = _kinds(plan)
    assert kinds[1][0] == "running"
    assert kinds[2] == ("scope", "same contract running: Serial with #1")


def test_an_open_pull_request_without_a_running_job_holds_only_its_own_issue():
    issues = [_issue(5, "feat(store): five"), _issue(6, "feat(store): six")]
    plan = picker.decide(_two(issues, pulls=[{"n": 50, "head": "feat/5-five", "body": "Closes #5"}]))
    kinds = _kinds(plan)
    assert kinds[5] == ("running", "already running: PR #50")
    assert kinds[6][0] == "open"
    busy = picker.decide(_two(issues, pulls=[{"n": 50, "head": "feat/5-five", "body": "Closes #5"}],
                              jobs=[{"id": "OT-5", "state": "running", "text": f"{REPO}#5"}]))
    assert _kinds(busy)[6] == ("scope", "same contract running: store #5")


def test_an_unreadable_dev_ci_holds_no_start_and_is_shown():
    """The dev CI read only says whether merges wait (a red dev never holds a start). When it fails after the
    issues, dependencies, pull requests and branches were read, the picks still open and the failure is shown; a
    shared repository error used to hold every issue as ``stopped`` and print \"nothing to open\"."""
    from hermes_cli.delivery.github import GhError
    from tests.hermes_cli._work_fakes import FakeGh

    def actions(_query):
        raise GhError("HTTP 502: Bad Gateway (actions/runs)")

    gh = FakeGh({f"repos/{REPO}/issues": [{"number": 7, "title": "feat(ui): seven", "body": "Sprint: M0 S3\n",
                                           "labels": [{"name": "omo"}],
                                           "issue_dependencies_summary": {"total_blocked_by": 0}}],
                 f"repos/{REPO}/pulls": [], f"repos/{REPO}/branches": [], f"repos/{REPO}/actions/runs": actions})
    repo = picker.collect_repo(gh, REPO, "dev", "CI")
    assert repo["error"] is None and [i["n"] for i in repo["issues"]] == [7]
    assert repo["dev_red"] is None and "502" in (repo["dev_error"] or "")
    snapshot = {"sprint": ("M0", 3), "repos": {REPO: repo}, "jobs": [], "runners": {"omo": "omo"}, "scopes": []}
    plan = picker.decide(snapshot)
    assert _kinds(plan)[7] == ("open", "")
    lines = picker.lines_of(plan)
    assert any(line.startswith(f"cannot read dev CI in {REPO}") and "merges wait" in line for line in lines), lines
    assert "nothing to open" not in lines


def _submit_lock_free(base: Path) -> bool:
    from hermes_cli.worker import settings as worker_settings
    from hermes_cli.worker.hub_store import Store
    with Store(worker_settings.root(base)).lock(picker.SUBMIT_LOCK, wait=False) as held:
        return held


def test_two_submitting_calls_on_one_snapshot_hand_each_issue_out_once(tmp_path):
    import threading
    _config(tmp_path, "work:\n  sprint: S1\n")
    submitted: list[tuple[str, str]] = []
    jobs: list[dict] = []
    probe: list[bool] = []

    class SharedRecord:
        def jobs(self):
            return list(jobs)

    class FakeHub:
        def __init__(self, name):
            self.name = name

        def submit(self, opts):
            if self.name == "first" and not submitted:
                # The second call starts while the first is between its snapshot and its hand-offs.
                checker = threading.Thread(target=lambda: probe.append(_submit_lock_free(tmp_path)))
                checker.start()
                checker.join(10)
                second.start()
            submitted.append((self.name, opts["topic"]))
            jobs.append({"id": f"OT-{len(jobs)}", "state": "running", "text": opts["topic"]})

    def call(name):
        picker.run_next(day_gh(recorded_day()), [REPO], SharedRecord(), submit_picks=True, base=tmp_path,
                        hub=FakeHub(name))

    second = threading.Thread(target=call, args=("second",))
    call("first")
    second.join(30)

    assert not second.is_alive()
    assert probe == [False], "the first call holds the submit lock while it hands out"
    topics = [topic for _, topic in submitted]
    assert len(topics) == len(set(topics)) == 5, submitted
    assert {name for name, _ in submitted} == {"first"}, "the second call saw the first one's jobs"


def test_listing_without_submit_takes_no_lock(tmp_path):
    from hermes_cli.worker import settings as worker_settings
    from hermes_cli.worker.hub_store import Store
    _config(tmp_path, "work:\n  sprint: S1\n")
    import threading
    result = []
    lister = threading.Thread(target=lambda: result.append(
        picker.run_next(day_gh(recorded_day()), [REPO], FakeRecord(), submit_picks=False, base=tmp_path)))
    with Store(worker_settings.root(tmp_path)).lock(picker.SUBMIT_LOCK):
        lister.start()
        lister.join(30)
        finished_while_held = not lister.is_alive()
    lister.join(30)
    assert finished_while_held, "a listing call must not wait on the submit lock"
    plan, ok = result[0]
    assert ok and [d["issue"] for d in plan["decisions"] if d["action"] == "open"] == [102, 116, 122, 125, 126]
