"""The ``report`` tool (lina#263): one call writes a report or a new version of it and returns the id, the page
address and the chat reference; every write reaches the app's change stream; a report written from a session with
a personal-document mark (lina#308) is private. Through the registry, on the real Postgres of
tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import importlib
import json
import sys
import types
from contextlib import closing

import pytest

import hermes_state_reports as reports
from gateway.report_reference import references
from hermes_cli import lina_deps
from tools.registry import registry

PUBLIC = "https://hub.example.ts.net"


@pytest.fixture
def lina_root(tmp_path, monkeypatch):
    importlib.import_module("tools.report_tool")
    root = tmp_path / "lina"
    monkeypatch.setattr(lina_deps, "lina_root", lambda: root)
    monkeypatch.setenv("HERMES_DASHBOARD_PUBLIC_URL", PUBLIC)
    return root


def _call(args: dict, session_id: str = "s-1") -> dict:
    return json.loads(registry.dispatch("report", args, session_id=session_id))


def _journal(root) -> list[dict]:
    path = root / "board" / "reports.jsonl"
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()] if path.exists() else []


def test_one_call_writes_a_report_and_another_with_its_id_adds_a_version(lina_root):
    made = _call({"title": "Weekly costs", "summary": "Costs fell 12% this week.", "body": "# Costs\n\nDetails.",
                  "card": "t_abc"})

    assert made["success"] and made["version"] == 1 and not made["private"]
    assert made["page"] == f"{PUBLIC}/reports/{made['id']}/page"
    assert references(made["reference"]) == [(made["id"], made["page"], "Costs fell 12% this week.")]

    again = _call({"id": made["id"], "title": "Weekly costs", "summary": "Costs fell 14% this week.",
                   "body": "# Costs\n\nCorrected."})
    assert (again["id"], again["version"]) == (made["id"], 2)
    with closing(reports.owner_connection()) as conn:
        assert reports.get(conn, made["id"]).body == "# Costs\n\nCorrected."
        assert reports.get(conn, made["id"], version=1).body == "# Costs\n\nDetails."
        assert reports.get(conn, made["id"]).card == "t_abc"
    assert [(e["kind"], e["id"]) for e in _journal(lina_root)] == [("created", made["id"]), ("updated", made["id"])]


def test_a_refused_write_is_a_tool_error_and_announces_nothing(lina_root):
    result = _call({"title": "Costs", "summary": "line one\n", "body": ""})
    assert "error" in result and "body" in result["error"]
    assert _journal(lina_root) == []


def test_a_report_from_a_session_with_a_personal_document_mark_is_private(lina_root, monkeypatch):
    marks = types.SimpleNamespace(marked_paths=lambda session: ["/docs/statement.pdf"] if session == "s-mark" else [])
    monkeypatch.setitem(sys.modules, "agent.personal_work",
                        types.SimpleNamespace(marks_for_home=lambda: marks))
    monkeypatch.setattr(sys.modules["agent"], "personal_work", sys.modules["agent.personal_work"], raising=False)

    assert _call({"title": "Bank", "summary": "Statement read.", "body": "Totals."}, session_id="s-mark")["private"]
    assert not _call({"title": "Plain", "summary": "Nothing personal.", "body": "Text."}, session_id="s-2")["private"]
    assert _call({"title": "Asked", "summary": "Marked by the call.", "body": "Text.", "private": True})["private"]
