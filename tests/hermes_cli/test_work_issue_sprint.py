"""``lina work next`` reads a ``- Sprint:`` list line like a bare ``Sprint:`` line (lina#511): issue bodies written
to the form in ``docs/delivery.md`` carry the sprint as a list item under Context, so both forms must name the
same sprint; a ``Sprint:`` line that names no sprint is held with a message saying what the line reads."""
from __future__ import annotations

import pytest

from hermes_cli.delivery import next as picker
from hermes_cli.delivery import settings

REPO = "lina-system/lina"


def _body(context: str) -> str:
    return f"## Summary\n\nx\n\n## Done when\n\n- [ ] y\n\n## Context\n\n{context}\n"


def _issue(n: int, title: str, context: str) -> dict:
    return {"n": n, "title": title, "body": _body(context), "labels": ["omo"], "parent": False}


def _snapshot(issues: list[dict]) -> dict:
    repo = {"issues": issues, "blocked_by": {}, "pulls": [], "branches": [], "dev_red": None, "dev_error": None,
            "error": None}
    return {"sprint": ("M0", 3), "repos": {REPO: repo}, "jobs": [], "runners": {"omo": "omo"}, "scopes": []}


def _plan(*issues: dict, **changes) -> dict:
    return picker.decide(dict(_snapshot(list(issues)), **changes))


def _decisions(plan: dict) -> dict:
    return {d["issue"]: (d["action"], d["kind"], d["reason"]) for d in plan["decisions"]}


@pytest.mark.parametrize("line, expected", [
    ("Sprint: M0 S3 — Work, worker and the client API (order in #1).", ("M0", 3)),
    ("- Sprint: M0 S3 — Work, worker and the client API (order in #1).", ("M0", 3)),
    ("* Sprint: M0 S3", ("M0", 3)),
    ("  - Sprint: M0 S3", ("M0", 3)),
    ("- Sprint: S3", (None, 3)),
    ("- Sprint: m0 s3", ("M0", 3)),
])
def test_a_sprint_line_names_the_same_sprint_with_or_without_a_list_marker(line, expected):
    assert settings.issue_sprint(_body(line)) == expected


def test_the_context_block_the_form_writes_names_the_sprint():
    """``docs/delivery.md`` writes Context as one list item per field, in this order."""
    body = _body("- Sprint: M0 S3 — Work\n- Depends on: none\n- Size: XS\n- Runner: omo-opus")
    assert settings.issue_sprint(body) == ("M0", 3)


def test_an_earlier_line_naming_no_sprint_does_not_hide_a_later_one():
    assert settings.issue_sprint(_body("- Sprint: none\n- Sprint: M0 S3")) == ("M0", 3)


@pytest.mark.parametrize("line, reads", [
    ("- Sprint: TBD", "reads 'TBD'"),
    ("- Sprint:", "is empty"),
    ("* Sprint: M0", "reads 'M0'"),
])
def test_a_sprint_line_that_names_no_sprint_is_shown_with_what_it_reads(line, reads):
    plan = _plan(_issue(1, "feat(ui): one", line))
    assert settings.issue_sprint(_body(line)) is None
    assert settings.sprint_line(_body(line)) == line.split(":", 1)[1].strip()
    assert _decisions(plan)[1][:2] == ("skip", "sprint")
    shown = [line for line in picker.lines_of(plan) if "#1" in line]
    assert shown == [f"hold {REPO}#1 (not in the current sprint: no sprint (the Sprint: line {reads}; "
                     "write Sprint: M0 S3 or Sprint: S3))"], shown


def test_a_body_with_no_sprint_line_stays_quiet_with_the_plain_no_sprint_reason():
    plan = _plan(_issue(1, "feat(ui): one", "- Size: XS"))
    assert settings.sprint_line(_body("- Size: XS")) is None
    assert _decisions(plan)[1][2] == "not in the current sprint: no sprint"
    assert not [line for line in picker.lines_of(plan) if "#1" in line]


def test_a_listed_sprint_line_opens_the_issue_in_the_current_sprint():
    """Before lina#511 every issue written to the form was held as ``no sprint``; it now opens like the bare form."""
    listed = _issue(1, "feat(ui): one", "- Sprint: M0 S3 — Work\n- Size: XS\n- Runner: omo")
    bare = _issue(2, "feat(ui): two", "Sprint: M0 S3\nSize: XS\nRunner: omo")
    assert _decisions(_plan(listed, bare)) == {1: ("open", "open", ""), 2: ("open", "open", "")}
    assert _decisions(_plan(listed, sprint=("M0", 4)))[1][2] == "not in the current sprint: M0 S3"


def test_size_reads_a_list_line_too_so_one_context_form_names_every_field():
    assert picker.size_of([], _body("- Size: M")) == "M"
    assert picker.size_of([], _body("Size: L")) == "L"


def test_serial_with_reads_a_list_line_too():
    """``Serial with:`` is the other Context field the picker reads from the body; a list item names it the same."""
    assert picker._serial(_body("- Serial with: #2"), REPO, [REPO]) == [(REPO, 2)]
    assert picker._serial(_body("Serial with: #2"), REPO, [REPO]) == [(REPO, 2)]
