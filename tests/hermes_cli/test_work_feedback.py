"""``lina work feedback <repo> <pr>`` (lina#522) through the real ``lina work`` parser against a fake GitHub: only the
current head's unresolved P0/P1 findings and failed required checks are printed, and nothing is written."""
from __future__ import annotations

import pytest

from hermes_cli.main import _build_cli_parser
from tests.hermes_cli._work_fakes import FakeGh

REPO, HEAD, OLD = "acme/app", "a" * 40, "b" * 40
CODEX = "chatgpt-codex-connector[bot]"


def finding(cid: int, priority: int, title: str, *, commit: str = HEAD, reply: int | None = None) -> dict:
    return {"id": cid, "user": {"login": CODEX}, "path": "app/x.py", "line": cid, "original_commit_id": commit,
            "commit_id": HEAD, "in_reply_to_id": reply,
            "body": f"**<sub><sub>![P{priority} Badge](https://img.shields.io/badge/P{priority}-orange)</sub></sub> "
                    f"{title}**\n\nWhy it matters."}


def run(name: str, conclusion: str, rid: int) -> dict:
    return {"id": rid, "name": name, "status": "completed", "conclusion": conclusion, "app": {"id": 15368}}


def github(comments: list[dict], runs: list[dict], resolved: tuple[int, ...] = ()) -> FakeGh:
    def threads(_query, _variables):
        nodes = [{"isResolved": True, "comments": {"nodes": [{"databaseId": cid}]}} for cid in resolved]
        return {"data": {"repository": {"pullRequest": {"reviewThreads": {
            "nodes": nodes, "pageInfo": {"hasNextPage": False}}}}}}

    log = "\n".join(f"\x1b[36;1mlog line {n}\x1b[0m" for n in range(1, 101)) + "\n"  # Actions colour codes
    return FakeGh({f"repos/{REPO}/pulls/31": {"number": 31, "head": {"sha": HEAD, "ref": "feat/12-x"},
                                              "base": {"ref": "dev"}},
                   f"repos/{REPO}/pulls/31/comments": comments,
                   f"repos/{REPO}/rules/branches/dev": [{"type": "required_status_checks", "parameters": {
                       "required_status_checks": [{"context": "tests"}, {"context": "lint"}]}}],
                   f"repos/{REPO}/commits/{HEAD}/check-runs": {"total_count": len(runs), "check_runs": runs},
                   f"repos/{REPO}/commits/{HEAD}/statuses": []},
                  graphql=threads, on_run=lambda args: log)


def feedback_cli(monkeypatch, capsys, gh: FakeGh) -> tuple[int, str]:
    from hermes_cli.delivery import github as github_module
    monkeypatch.setattr(github_module, "Gh", lambda: gh)
    parser, _ = _build_cli_parser()
    args = parser.parse_args(["work", "feedback", REPO, "31"])
    with pytest.raises(SystemExit) as ended:
        args.func(args)
    return ended.value.code, capsys.readouterr().out


def test_feedback_prints_the_heads_open_p0_p1_findings_and_failed_required_check_logs(monkeypatch, capsys):
    comments = [finding(1, 1, "Open finding on the head"), finding(2, 0, "Resolved finding"),
                finding(3, 1, "Finding on an older head", commit=OLD), finding(4, 2, "Minor finding"),
                finding(5, 1, "A reply", reply=1)]
    runs = [run("tests", "failure", 70), run("lint", "success", 71), run("extra", "failure", 72)]
    gh = github(comments, runs, resolved=(2,))

    code, out = feedback_cli(monkeypatch, capsys, gh)

    assert code == 0
    assert f"P1 {CODEX} app/x.py:1\n" in out and "Open finding on the head" in out
    for absent in ("Resolved finding", "older head", "Minor finding", "A reply", "lint", "extra"):
        assert absent not in out
    assert "Failed required check tests (failure)" in out
    tail = out.split("log lines:\n", 1)[1].splitlines()
    assert tail[0] == "log line 21" and tail[79] == "log line 100" and len(tail) == 80
    assert gh.writes == []
    assert gh.commands == [["api", "--allow-escape-sequences", f"repos/{REPO}/actions/jobs/70/logs"]]


def test_feedback_prints_nothing_and_exits_0_when_there_is_nothing_to_fix(monkeypatch, capsys):
    gh = github([finding(3, 1, "Finding on an older head", commit=OLD)],
                [run("tests", "success", 70), run("lint", "success", 71)])

    assert feedback_cli(monkeypatch, capsys, gh) == (0, "")
    assert gh.writes == [] and gh.commands == []
