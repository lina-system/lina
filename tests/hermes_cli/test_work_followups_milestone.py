"""Review follow-ups go to the next milestone (lina#436): the source milestone is the closed issue's, the destination
is the next open milestone in roadmap order, one bundle per area and destination with that milestone set. The current
sprint plays no part; no source or no open destination falls back to the area's bundle without a milestone. GitHub
is a fake: nothing here creates an issue."""
from __future__ import annotations

import threading

import pytest

from hermes_cli.delivery import followups, settings
from tests.hermes_cli._work_fakes import FakeGh

REPO, HEAD, OLD = "acme/app", "h" * 40, "o" * 40
CODEX = {"login": "chatgpt-codex-connector[bot]"}
M0 = {"number": 1, "title": "M0 One store", "state": "open"}
M1 = {"number": 2, "title": "M1 Memory with boundaries", "state": "open"}
M2 = {"number": 3, "title": "M2 Records", "state": "open"}
LATER = {"number": 5, "title": "Later", "state": "open"}
TITLE = "chore(hermes_cli): review follow-ups"


def finding(cid, head, title, line=3, level="P2"):
    return {"id": cid, "user": CODEX, "path": "hermes_cli/x.py", "line": line, "commit_id": head,
            "original_commit_id": head,
            "body": f"**<sub><sub>![{level} Badge](https://img.shields.io/badge/{level}-x)</sub></sub>  {title}**\n\nmore"}


@pytest.fixture(autouse=True)
def _isolated(tmp_path, monkeypatch):
    from hermes_cli.worker import settings as worker_settings
    monkeypatch.setattr(worker_settings, "root", lambda base=None: tmp_path / "lina")
    monkeypatch.setattr(settings, "followup_two_level", lambda base=None: ())


def github(comments, *, milestone=M0, milestones=(M0, M1, M2, LATER), issues=None, resolved=(), pr=7,
           body="## Summary\n\nx\n\nCloses #12\n"):
    """A fake whose issue writes land in ``issues`` (the open follow-up list), so a re-run reads what was written."""
    store = issues if issues is not None else []
    routes = {f"repos/{REPO}/pulls/{pr}": {"number": pr, "body": body},
              f"repos/{REPO}/pulls/{pr}/comments": comments,
              f"repos/{REPO}/issues/12": {"number": 12, "milestone": milestone},
              f"repos/{REPO}/milestones": list(milestones),
              f"repos/{REPO}/issues": lambda _q: [dict(i) for i in store]}
    threads = {"pageInfo": {"hasNextPage": False},
               "nodes": [{"isResolved": True, "comments": {"nodes": [{"databaseId": i}]}} for i in resolved]}
    gh = FakeGh(routes, graphql=lambda q, v: {"data": {"repository": {"pullRequest": {"reviewThreads": threads}}}})

    def write(method, path, payload=None):
        gh.writes.append((method, path, payload))
        if method == "POST":
            made = {"number": 900 + len(store), "title": payload["title"], "body": payload["body"],
                    "milestone": next((m for m in milestones if m["number"] == payload.get("milestone")), None)}
            store.append(made)
            return made
        number = int(path.rsplit("/", 1)[1])
        next(i for i in store if i["number"] == number)["body"] = payload["body"]
        return {"number": number}

    gh.write = write
    return gh, store


def test_m0_findings_go_to_m1_with_the_milestone_set_whatever_the_current_sprint(monkeypatch):
    monkeypatch.setattr(settings, "sprint", lambda base=None: ("M1", 2))
    gh, store = github([finding(71, HEAD, "Guard the empty case")])
    assert followups.file(gh, REPO, 7, HEAD) == [f"{TITLE}: opened #900 with 1"]
    (method, path, payload), = gh.writes
    assert (method, path, payload["milestone"], payload["labels"]) == ("POST", f"repos/{REPO}/issues", 2, ["follow-up"])
    assert "Milestone: M1 Memory with boundaries" in payload["body"] and "Sprint:" not in payload["body"]

    monkeypatch.setattr(settings, "sprint", lambda base=None: ("M0", 3))
    gh2, _ = github([finding(72, HEAD, "Name the file", line=8)], pr=8, issues=store)
    assert followups.file(gh2, REPO, 8, HEAD) == [f"{TITLE}: added 1 to #900"]
    assert [w[0] for w in gh2.writes] == ["PATCH"] and len(store) == 1, "the M1 bundle is extended, not duplicated"


def test_an_open_bundle_of_another_milestone_is_not_the_destination_but_its_boxes_count():
    m0_bundle = {"number": 40, "title": TITLE, "milestone": M0,
                 "body": "## Done when\n\n- [ ] P2 Guard the empty case — hermes_cli/x.py:3 (from #5)\n"}
    gh, _ = github([finding(71, HEAD, "Guard the empty case"), finding(72, HEAD, "Name the file", line=8)],
                       issues=[m0_bundle])
    assert followups.file(gh, REPO, 7, HEAD) == [f"{TITLE}: opened #901 with 1"]
    assert gh.writes[0][2]["milestone"] == 2 and "Name the file" in gh.writes[0][2]["body"]
    assert "Guard the empty case" not in gh.writes[0][2]["body"], "already tracked in the M0 bundle"


def test_a_closed_next_milestone_is_skipped_for_the_next_open_one_in_roadmap_order():
    gh, _ = github([finding(71, HEAD, "Guard the empty case")],
                   milestones=(LATER, M2, {**M1, "state": "closed"}, M0))
    followups.file(gh, REPO, 7, HEAD)
    assert gh.writes[0][2]["milestone"] == 3 and "Milestone: M2 Records" in gh.writes[0][2]["body"]


@pytest.mark.parametrize("over", [{"milestone": None}, {"body": "no closing line"},
                                  {"milestone": M2, "milestones": (M0, M1, M2, LATER)}],
                         ids=["issue-without-milestone", "pr-closes-no-issue", "no-open-milestone-after"])
def test_without_a_source_or_destination_the_fallback_is_the_area_bundle_without_a_milestone(over):
    gh, store = github([finding(71, HEAD, "Guard the empty case")], **over)
    assert followups.file(gh, REPO, 7, HEAD) == [f"{TITLE}: opened #900 with 1"]
    payload = gh.writes[0][2]
    assert "milestone" not in payload and "Milestone: none" in payload["body"]

    gh2, _ = github([finding(72, HEAD, "Name the file", line=8)], issues=store, **over)
    assert followups.file(gh2, REPO, 7, HEAD) == [f"{TITLE}: added 1 to #900"]


def test_a_replay_adds_nothing_and_a_resolved_finding_is_never_filed():
    comments = [finding(71, HEAD, "Guard the empty case"), finding(72, HEAD, "Resolved one", line=8)]
    gh, store = github(comments, resolved=(72,))
    followups.file(gh, REPO, 7, HEAD)
    assert "Resolved one" not in store[0]["body"]
    gh2, _ = github(comments, resolved=(72,), issues=store)
    assert followups.file(gh2, REPO, 7, HEAD) == [f"{TITLE}: every finding already listed"]
    assert gh2.writes == []


def test_two_merges_filing_into_one_milestone_bundle_keep_both_findings():
    """Both runs read the M1 bundle before either writes unless the hub lock serializes read and write."""
    store = [{"number": 40, "title": TITLE, "milestone": M1, "body": "## Done when\n\n## Context\n\nMilestone: M1\n"}]
    read_both = threading.Barrier(2, timeout=2)

    def run(pr, title):
        gh, _ = github([finding(70 + pr, HEAD, title, line=pr)], pr=pr, issues=store)
        listing = gh.routes[f"repos/{REPO}/issues"]

        def issues(query):
            rows = listing(query)
            try:
                read_both.wait()
            except threading.BrokenBarrierError:
                pass  # serialized: the other run already finished
            return rows
        gh.routes[f"repos/{REPO}/issues"] = issues
        results[pr] = followups.file(gh, REPO, pr, HEAD)

    results = {}
    threads = [threading.Thread(target=run, args=args) for args in ((7, "Guard A"), (8, "Guard B"))]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(10)
    assert len(store) == 1 and "Guard A" in store[0]["body"] and "Guard B" in store[0]["body"], store
    assert all(lines == [f"{TITLE}: added 1 to #40"] for lines in results.values()), results


def test_an_unresolved_finding_on_an_older_head_is_filed_or_reported_not_dropped():
    tracked = {"number": 40, "title": TITLE, "milestone": M1,
               "body": "## Done when\n\n- [ ] P3 Already tracked — hermes_cli/x.py:5 (from #6)\n"}
    comments = [finding(71, OLD, "Still open on the old head", level="P3"),
                finding(72, OLD, "Already tracked", line=5, level="P3"),
                finding(73, OLD, "Fixed and resolved", line=9),
                finding(74, HEAD, "On the merged head", line=11)]
    gh, store = github(comments, issues=[tracked], resolved=(73,))
    lines = followups.file(gh, REPO, 7, HEAD)
    assert lines == [f"{TITLE}: added 2 to #40",
                     "#7: P2/P3 findings on older heads: 1 resolved, 1 already tracked, 1 filed"]
    body = store[0]["body"]
    assert "Still open on the old head" in body and "On the merged head" in body
    assert body.count("Already tracked") == 1 and "Fixed and resolved" not in body
