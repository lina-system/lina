"""One ``lina work`` parser (lina#63 joined to lina#36): through the real ``main`` parser, ``next``, ``merge``,
``status`` and ``sprint current`` reach the delivery code, ``sprint set|owner|show <id>`` still reach the work
record's sprint checklist, and ``lina work`` with no verb is a usage error as on lina#36."""
from __future__ import annotations

import pytest

from hermes_cli.main import _build_cli_parser


def _parse(*argv):
    parser, _ = _build_cli_parser()
    return parser.parse_args(["work", *argv])


@pytest.mark.parametrize("argv, name, seen", [
    (["next", "--repo", "acme/app", "--submit", "--json"], "cmd_next",
     {"repo": ["acme/app"], "submit": True, "json": True}),
    (["merge", "acme/app", "7"], "cmd_merge", {"target": ["acme/app", "7"], "branch": None}),
    (["merge", "--branch", "feat/12-x", "--task", "OT-1", "title", "12"], "cmd_merge",
     {"branch": "feat/12-x", "task": "OT-1", "target": ["title", "12"]}),
    (["status", "--repo", "acme/app"], "cmd_status", {"repo": ["acme/app"]}),
    (["sprint", "current"], "cmd_sprint_current", {"value": None}),
    (["sprint", "current", "M0 S3"], "cmd_sprint_current", {"value": "M0 S3"}),
])
def test_delivery_verbs_reach_the_delivery_code_with_their_exit_code(monkeypatch, argv, name, seen):
    from hermes_cli.delivery import cli
    calls = []

    def fake(args):
        calls.append({key: getattr(args, key) for key in seen})
        raise SystemExit(75)

    monkeypatch.setattr(cli, name, fake)
    args = _parse(*argv)
    with pytest.raises(SystemExit) as ended:
        args.func(args)
    assert ended.value.code == 75
    assert calls == [seen]


def test_sprint_current_sets_and_shows_the_owners_sprint(capsys):
    for argv in (["sprint", "current", "m0 s2"], ["sprint", "current"]):
        args = _parse(*argv)
        with pytest.raises(SystemExit) as ended:
            args.func(args)
        assert ended.value.code == 0
    assert capsys.readouterr().out.splitlines() == ["current sprint: M0 S2", "current sprint: M0 S2"]


def test_the_sprint_checklist_verbs_keep_the_work_record_meaning(monkeypatch):
    from hermes_cli.work import cmd_edit
    calls = []
    monkeypatch.setattr(cmd_edit, "cmd_sprint", lambda args: calls.append((args.action, args.id, args.item,
                                                                            args.state)))
    args = _parse("sprint", "set", "T1", "--item", "write the note", "--state", "done")
    args.fn(args)
    assert calls == [("set", "T1", "write the note", "done")]


def test_work_without_a_verb_is_a_usage_error():
    with pytest.raises(SystemExit) as ended:
        _parse()
    assert ended.value.code == 2
