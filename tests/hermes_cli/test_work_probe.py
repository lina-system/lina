"""``lina work probe``: read-only checks answer 0 holds, 1 not yet, 2 cannot tell, with one evidence line (lina#38)."""

import json
import os
import stat

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv


def fake(tmp_path, monkeypatch, name: str, body: str) -> None:
    """A command on PATH that logs its arguments and runs *body* (sh)."""
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir(exist_ok=True)
    path = bin_dir / name
    path.write_text(f'#!/bin/sh\necho "$@" >> "{bin_dir}/{name}.calls"\n{body}\n', encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IEXEC)
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")


def probe(work: WorkEnv, expected: int, *args: str) -> str:
    code, out, err = work.run("probe", *args)
    assert code == expected, (args, code, out, err)
    assert len(out.strip().splitlines()) >= 1
    return out.strip()


@pytest.mark.parametrize(("payload", "rc"), [
    ({"state": "closed"}, 0),
    ({"state": "open"}, 1),
    ({"state": "closed", "pull_request": {"merged_at": "2026-01-01T00:00:00Z"}}, 0),
    ({"state": "closed", "pull_request": {"merged_at": None}}, 1),
])
def test_gh_closed_needs_closed_issues_and_merged_pull_requests(tmp_path, monkeypatch, payload, rc):
    # Given
    work = WorkEnv(tmp_path, monkeypatch, extra="  github: {owner: example}\n")
    fake(tmp_path, monkeypatch, "gh", f"echo '{json.dumps(payload)}'")
    # When
    line = probe(work, rc, "gh-closed", "demo#1")
    # Then
    assert "demo#1" in line
    assert (tmp_path / "bin/gh.calls").read_text().split("\n")[0] == "api repos/example/demo/issues/1"


def test_gh_closed_cannot_tell_when_github_or_the_owner_is_unknown(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch, extra="  github: {owner: example}\n")
    fake(tmp_path, monkeypatch, "gh", "exit 1")
    # When / Then
    assert "demo#1" in probe(work, 2, "gh-closed", "demo#1")
    work.config("ko")
    assert "demo" in probe(work, 2, "gh-closed", "demo#1")
    assert probe(work, 2, "gh-closed", "demo") != ""


@pytest.mark.parametrize(("opened", "closed", "rc"), [(0, 3, 0), (1, 3, 1), (0, 0, 1)])
def test_ms_closed_counts_open_issues(tmp_path, monkeypatch, opened, closed, rc):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    fake(tmp_path, monkeypatch, "gh", "echo '" + json.dumps(
        {"title": "M0", "open_issues": opened, "closed_issues": closed}) + "'")
    # When
    line = probe(work, rc, "ms-closed", "example/demo/1")
    # Then
    assert line == f"M0: {closed}/{opened + closed} 닫힘"


def test_file_and_fresh(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    path = tmp_path / "fixture.txt"
    # When / Then
    probe(work, 1, "file", str(path))
    probe(work, 1, "fresh", str(path), "1")
    path.write_text("fixture is ready", encoding="utf-8")
    probe(work, 0, "file", str(path))
    probe(work, 0, "file", str(path), "is ready$")
    probe(work, 1, "file", str(path), "missing")
    probe(work, 2, "file", str(path), "(")
    os.utime(path, (NOW - 600, NOW - 600))
    probe(work, 0, "fresh", str(path), "1")
    os.utime(path, (NOW - 7200, NOW - 7200))
    assert probe(work, 1, "fresh", str(path), "1") == "fixture.txt 2.0시간 전 수정"
    probe(work, 2, "fresh", str(path), "soon")


def test_session_ok_reads_the_configured_queue(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    done = work.queue / "done"
    # When / Then
    probe(work, 1, "session-ok", "alpha:fixture")
    done.mkdir(parents=True)
    record = {"profile": "alpha", "name": "fixture", "ended": NOW, "state": "exited", "rc": 0}
    (done / "old.json").write_text(json.dumps(record))
    probe(work, 0, "session-ok", "alpha:fixture")
    (done / "new.json").write_text(json.dumps(dict(record, ended=NOW + 1, rc=1)))
    probe(work, 1, "session-ok", "alpha:fixture")
    (work.queue / "running").mkdir()
    (work.queue / "running/live.json").write_text(json.dumps(record))
    assert probe(work, 0, "session-ok", "alpha:fixture") == "fixture 도는 중"
    probe(work, 2, "session-ok", "alpha")


def test_session_ok_cannot_tell_without_a_queue(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    config = work.root / "config.yaml"
    config.write_text("".join(line for line in config.read_text(encoding="utf-8").splitlines(True)
                              if "session_queue" not in line), encoding="utf-8")
    # When / Then
    assert probe(work, 2, "session-ok", "alpha:fixture") == "세션 큐가 없다 (work.session_queue)"


def test_todoist_done_runs_the_configured_probe(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    # When / Then: no probe configured
    probe(work, 2, "todoist-done", "123")
    probe_cmd = tmp_path / "bin/todo-probe"
    work.config("ko", f"  probes: {{todoist: [{probe_cmd}, get]}}\n")
    for exit_code, rc in ((0, 0), (1, 1), (3, 2)):
        fake(tmp_path, monkeypatch, "todo-probe", f"exit {exit_code}")
        probe(work, rc, "todoist-done", "todoist:123")
    assert (tmp_path / "bin/todo-probe.calls").read_text().splitlines()[0] == "get 123"


def test_worker_running_counts_running_jobs_per_device(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    jobs = work.root / "worker/hub/jobs"
    jobs.mkdir(parents=True)
    for n, (host, state) in enumerate((("build-a", "running"), ("build-b", "done"))):
        (jobs / f"OT-{n}.json").write_text(json.dumps({"id": f"OT-{n}", "host": host, "state": state,
                                                       "created": NOW - n}))
    # When / Then
    assert probe(work, 0, "worker-running", "build-a") == "build-a=1"
    assert probe(work, 1, "worker-running", "build-a", "build-b") == "build-a=1 build-b=0"


def test_agents_goals_counts_only_goals_an_agent_wrote_itself(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    from hermes_cli import profiles
    monkeypatch.setattr(profiles, "get_active_profile_name", lambda: "alpha")
    for by in ("alpha", "beta"):
        work.ok("add", "목표", "--by", by, "--area", "Dev", "--title", f"{by} 목표", "--basis", "정리해 줘",
                "--done", "정리된다", "--check", "false")
    # When / Then: alpha wrote its own goal, beta's was seeded by alpha
    assert probe(work, 0, "agents-goals", "1") == "목표를 직접 넣은 에이전트 1: 알파"
    probe(work, 1, "agents-goals", "2")
    probe(work, 2, "agents-goals", "two")


def test_usage_errors_cannot_tell_and_probes_never_write_the_record(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    # When
    codes = [work.run("probe", *args)[0] for args in ((), ("unknown",), ("gh-closed",), ("fresh", "x"))]
    probe(work, 1, "file", str(tmp_path / "missing"))
    # Then
    assert codes == [2, 2, 2, 2]
    assert not (work.work / "state.json").exists()


def test_a_timeout_cannot_tell(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch, extra="  github: {owner: example}\n")
    from hermes_cli.work import probe as probes
    monkeypatch.setattr(probes, "TIMEOUT", 0.2)
    fake(tmp_path, monkeypatch, "gh", "sleep 5")
    # When / Then
    assert probe(work, 2, "gh-closed", "demo#1") == "시간 초과"


def test_english_locale_lines(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch, locale="en")
    # When / Then
    assert probe(work, 1, "file", str(tmp_path / "nope")) == "missing: nope"
