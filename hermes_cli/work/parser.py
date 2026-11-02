"""The ``lina work`` argument parser: every verb and option of the work ledger, plus ``import``, ``run`` and
``card``. Help texts are English; outputs follow ``work.locale``."""

from __future__ import annotations

import argparse

from hermes_cli.work.locale import REPLY_ALIASES, REPLY_RESULTS


def _fn(module: str, name: str):
    def call(a):
        from importlib import import_module
        return getattr(import_module(f"hermes_cli.work.{module}"), name)(a)
    return call


def _as(p) -> None:
    p.add_argument("--as", dest="as_", help="the agent writing this (default: the item's agent)")


def _sprint(sp) -> None:
    p = sp.add_parser("sprint", help="sprint checklist on a to-do")
    actions = p.add_subparsers(dest="action", required=True)
    for action in ("set", "owner", "show"):
        q = actions.add_parser(action)
        q.add_argument("id")
        if action == "set":
            q.add_argument("--item", required=True)
            q.add_argument("--state", choices=("done", "doing", "todo"), required=True)
            q.add_argument("--now")
        if action == "owner":
            q.add_argument("text", nargs="?")
            q.add_argument("--none", action="store_true")
        if action != "show":
            _as(q)
        q.set_defaults(fn=_fn("cmd_edit", "cmd_sprint"))


def _add(sp) -> None:
    p = sp.add_parser("add", help="add an area, goal, to-do or memo (issues: work link)")
    p.add_argument("kind", help="영역|목표|할일|메모 or area|goal|todo|memo")
    p.add_argument("--area", help="area of a goal or memo (one of work.areas, optionally one level below)")
    p.add_argument("--project", action="append", help="Desk 1_Projects folder of a goal (repeatable or a,b)")
    p.add_argument("--from", dest="from_", help="goal: the memo it comes from · owner's to-do: the agent that asked")
    p.add_argument("--goal", help="the goal a to-do belongs to")
    p.add_argument("--by", required=True, help="the agent it is assigned to, or the owner (owner's own to-do)")
    p.add_argument("--due", help="due date of a goal or to-do, YYYY-MM-DD (optional)")
    p.add_argument("--basis", "--quote", dest="basis", action="append", help="the owner's words (repeatable)")
    p.add_argument("--done")
    p.add_argument("--check")
    p.add_argument("--title", help="normalized title: a short noun phrase naming the result")
    p.add_argument("--parent")
    p.add_argument("--session")
    p.add_argument("--omo", help="the worker run (OT-…) that works the to-do, linked as omo:OT-…; next: one comes soon")
    p.add_argument("--link", action="append")
    p.add_argument("--promise", action="store_true")
    p.add_argument("--said")
    p.add_argument("--every")
    p.add_argument("--stall", type=float)
    p.add_argument("--note")
    p.add_argument("--why", help="required on a new to-do: what gets better for the owner when it is done")
    p.add_argument("--via", choices=("board",), help="board: a memo the owner handed out from the dashboard")
    p.set_defaults(fn=_fn("cmd_add", "cmd_add"))


def _item_writes(sp) -> None:
    for name, fn, extra in (("link", "cmd_link", "links"), ("unlink", "cmd_unlink", "links"),
                            ("evidence", "cmd_evidence", "refs")):
        p = sp.add_parser(name)
        p.add_argument("id")
        p.add_argument(extra, nargs="+")
        _as(p)
        p.set_defaults(fn=_fn("cmd_items", fn))
    p = sp.add_parser("note")
    p.add_argument("id")
    p.add_argument("text")
    _as(p)
    p.set_defaults(fn=_fn("cmd_items", "cmd_note"))
    p = sp.add_parser("wait")
    p.add_argument("id")
    p.add_argument("--on")
    p.add_argument("--next")
    p.add_argument("--clear", action="store_true")
    p.add_argument("--hold", action="store_true")
    p.add_argument("--decide", help="one line the owner decides (empty: clear it and its choices)")
    p.add_argument("--choice", action="append", help="an answer to choose (repeatable)")
    p.add_argument("--recommend", help="the recommended --choice")
    p.add_argument("--on-item", help="released when this to-do ends: an agent's to-do waits on an owner's to-do")
    _as(p)
    p.set_defaults(fn=_fn("cmd_items", "cmd_wait"))
    p = sp.add_parser("confirm", help='close by confirmation: --quote "<owner\'s words>" or --by <agent> --seen "<what>"')
    p.add_argument("id")
    p.add_argument("--quote")
    p.add_argument("--by")
    p.add_argument("--seen")
    p.set_defaults(fn=_fn("cmd_items", "cmd_confirm"))
    for name, fn, flag, required in (("drop", "cmd_drop", "--quote", False), ("reopen", "cmd_reopen", "--why", True)):
        p = sp.add_parser(name)
        p.add_argument("id")
        p.add_argument(flag, required=required)
        _as(p)
        p.set_defaults(fn=_fn("cmd_items", fn))
    p = sp.add_parser("undo", help="append an earlier revision as the next one")
    p.add_argument("id")
    p.add_argument("--to", type=int)
    p.add_argument("--why", required=True)
    p.add_argument("--expect", type=int, help=argparse.SUPPRESS)
    _as(p)
    p.set_defaults(fn=_fn("cmd_items", "cmd_undo"))


def _comments(sp) -> None:
    p = sp.add_parser("comment", help="comment on a goal or to-do (its agent hears one line)")
    p.add_argument("id")
    p.add_argument("text")
    p.add_argument("--by", help="writer (default: the owner)")
    p.add_argument("--answer", action="store_true", help="an answer to the item's open decision")
    p.set_defaults(fn=_fn("cmd_comments", "cmd_comment"))
    p = sp.add_parser("reply", help="the item agent's reply to a comment")
    p.add_argument("id")
    p.add_argument("cid")
    p.add_argument("text")
    p.add_argument("--result", choices=REPLY_RESULTS + tuple(REPLY_ALIASES), required=True)
    p.add_argument("--by")
    p.set_defaults(fn=_fn("cmd_comments", "cmd_reply"))
    p = sp.add_parser("owner-done", help="the owner marks it done")
    p.add_argument("id")
    p.add_argument("--via", choices=("board",))
    p.add_argument("--with-open", action="store_true", help="also mark the goal's open to-dos done")
    p.set_defaults(fn=_fn("cmd_comments", "cmd_owner_done"))
    p = sp.add_parser("owner-undo", help="undo the owner's done mark within 10 minutes")
    p.add_argument("id")
    p.set_defaults(fn=_fn("cmd_comments", "cmd_owner_undo"))


def _edit(sp) -> None:
    p = sp.add_parser("edit")
    p.add_argument("id")
    for k in ("title", "done", "check", "session", "parent", "every", "owner"):
        p.add_argument("--" + k)
    p.add_argument("--why")
    p.add_argument("--project", action="append", help="replace the goal's projects (empty: none)")
    p.add_argument("--add-project", action="append")
    p.add_argument("--remove-project", action="append")
    p.add_argument("--quote", help="replace one basis entry (the old one stays in quote_history; needs --source)")
    p.add_argument("--source")
    p.add_argument("--basis", action="append")
    p.add_argument("--said")
    p.add_argument("--decide")
    p.add_argument("--due", help="due date YYYY-MM-DD; empty clears it")
    p.add_argument("--stall", type=float)
    _as(p)
    p.set_defaults(fn=_fn("cmd_edit", "cmd_edit"))
    p = sp.add_parser("archive")
    p.add_argument("id")
    p.add_argument("--moved", action="store_true")
    _as(p)
    p.set_defaults(fn=_fn("cmd_edit", "cmd_archive"))


def _reads(sp) -> None:
    p = sp.add_parser("check")
    p.add_argument("ids", nargs="*")
    p.add_argument("--all", action="store_true")
    p.add_argument("--notify", action="store_true")
    p.add_argument("--dry", action="store_true")
    p.add_argument("--quiet", action="store_true")
    p.set_defaults(fn=_fn("cmd_read", "cmd_check"))
    p = sp.add_parser("list")
    for flag in ("--by", "--area", "--project", "--kind"):
        p.add_argument(flag)
    p.add_argument("--all", action="store_true")
    p.set_defaults(fn=_fn("cmd_read", "cmd_list"))
    p = sp.add_parser("show")
    p.add_argument("id")
    p.set_defaults(fn=_fn("cmd_read", "cmd_show"))
    p = sp.add_parser("verify-basis", help="look the basis up in the agents' state.db (read-only)")
    p.add_argument("id", nargs="?")
    p.add_argument("--all", action="store_true")
    p.set_defaults(fn=_fn("cmd_read", "cmd_verify_basis"))
    p = sp.add_parser("json")
    p.add_argument("--all", action="store_true")
    p.set_defaults(fn=_fn("cmd_read", "cmd_json"))
    p = sp.add_parser("lint", help="texts that break the writing rules (read-only)")
    p.add_argument("--by")
    p.add_argument("--open", action="store_true")
    p.set_defaults(fn=_fn("cmd_read", "cmd_lint"))
    p = sp.add_parser("fields", help="what each dashboard field holds, how long, in what shape")
    p.add_argument("name", nargs="?")
    g = p.add_mutually_exclusive_group()
    g.add_argument("--markdown", action="store_true")
    g.add_argument("--json", action="store_true")
    g.add_argument("--prompt", metavar="FIELD")
    g.add_argument("--check", metavar="TEXT")
    p.set_defaults(fn=_fn("cmd_read", "cmd_fields"), no_resume=True)
    p = sp.add_parser("plain", help="check a generator's text against a field's rules, retry once, else drop it")
    p.add_argument("name")
    p.add_argument("--context")
    p.add_argument("--timeout", type=float, default=120)
    p.add_argument("cmd", nargs="*", help="after --: the generator command (stdin: prompt, stdout: one line)")
    p.set_defaults(fn=_fn("cmd_read", "cmd_plain"), no_resume=True)


def _moves(sp) -> None:
    p = sp.add_parser("migrate", help="a ledger.jsonl -> a new SQLite work.db file, checked")
    p.add_argument("--from", dest="from_", required=True)
    p.add_argument("--to", required=True)
    p.add_argument("--archive", help=argparse.SUPPRESS)
    p.add_argument("--omo-jobs", help=argparse.SUPPRESS)
    p.set_defaults(fn=_fn("cmd_store", "cmd_migrate"), no_resume=True)
    p = sp.add_parser("export-jsonl", help="the store (or a work.db) as ledger.jsonl")
    p.add_argument("--from", dest="from_")
    p.add_argument("--to")
    p.add_argument("--archive", help=argparse.SUPPRESS)
    p.set_defaults(fn=_fn("cmd_store", "cmd_export_jsonl"), no_resume=True)
    p = sp.add_parser("freeze", help="writers wait (never fail) until thaw")
    p.add_argument("--why", default="moving the record")
    p.set_defaults(fn=_fn("cmd_store", "cmd_freeze"), no_resume=True)
    p = sp.add_parser("thaw")
    p.set_defaults(fn=_fn("cmd_store", "cmd_thaw"), no_resume=True)


def build(sp) -> None:
    for part in (_sprint, _add, _item_writes, _comments, _edit, _reads, _moves):
        part(sp)
