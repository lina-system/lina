"""lina#438: ledger goals as generated brain project pages, linked both ways, with status read from the ledger only.

The ledger is a scratch SQLite work record (tests/hermes_cli/_work_env.py); the brain is the real Postgres from
tests/_fixtures/memory_store.py. Wiki and goals are synthetic.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import unicodedata
from pathlib import Path

import psycopg
import pytest

from hermes_cli.brain.lint import lint
from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
from hermes_cli.brain.scan import scan
from hermes_cli.subcommands.brain import build_brain_parser
from tests.hermes_cli._work_env import WorkEnv

WIKI = {
    "people/미나.md": "# 미나\n\nProducer.\n",
    "projects/샘플 앨범.md": "---\naliases: [Sample Album]\n---\n# 샘플 앨범\n\nWith [[미나]].\n",
    "concepts/mixing.md": "# Mixing\n\nA method.\n",
}


def _brain(capsys, *argv: str) -> tuple[int, str]:
    parser = argparse.ArgumentParser()
    build_brain_parser(parser.add_subparsers())
    args = parser.parse_args(["brain", *argv])
    code = args.func(args)
    captured = capsys.readouterr()
    return code, captured.out + captured.err


def _json(capsys, *argv: str) -> dict:
    code, out = _brain(capsys, *argv, "--json")
    assert code == 0, out
    return json.loads(out)


def _hashes(root: Path) -> dict[str, str]:
    return {path.relative_to(root).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in sorted(root.rglob("*")) if path.is_file()}


def _edges(memory_store, ref_prefix: str = "ledger:") -> set[tuple[str, str, str]]:
    with psycopg.connect(memory_store.superuser_dsn()) as conn:
        rows = conn.execute("SELECT from_slug, to_slug, source_ref FROM brain.edges WHERE source_ref LIKE %s",
                            (ref_prefix + "%",)).fetchall()
    return {tuple(row) for row in rows}


@pytest.fixture
def home(tmp_path, monkeypatch, _memory_store, capsys):
    wiki = tmp_path / "wiki"
    for name, body in WIKI.items():
        (wiki / name).parent.mkdir(parents=True, exist_ok=True)
        (wiki / name).write_text(body, encoding="utf-8")
    work = WorkEnv(tmp_path, monkeypatch, extra=f"brain: {{work_dir: {tmp_path / 'brain-work'}}}\n")
    work.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "샘플 앨범 발매", "--basis", "앨범 내자",
            "--done", "미나와 함께 발매한다", "--check", "false")
    work.ok("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", "[[Mixing]] 마무리", "--why", "음이 고르게",
            "--done", "믹스가 끝난다", "--check", "false", "--omo", "next")
    work.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "빌드 기기 정리", "--basis", "정리해 줘",
            "--done", "기기가 답한다", "--check", "false")
    code, out = _brain(capsys, "import", str(wiki))
    assert code == 0, out
    work.wiki, work.pages = wiki, tmp_path / "brain-work" / "projects"
    return work


def test_sync_writes_a_page_per_goal_linked_both_ways_and_a_second_sync_changes_nothing(home, _memory_store, capsys):
    wiki_before = _hashes(home.wiki)

    first = _json(capsys, "projects", "sync")

    assert first["pages_created"] == ["goal-g1", "goal-g2"] and first["pages_updated"] == []
    assert sorted(path.name for path in home.pages.iterdir()) == ["goal-g1.md", "goal-g2.md"]
    page = (home.pages / "goal-g1.md").read_text(encoding="utf-8")
    assert "title: 샘플 앨범 발매" in page and "ledger_goal: G1" in page
    assert "{{ledger:G1:status}}" in page and "- T1 Mixing 마무리: {{ledger:T1:status}}" in page
    assert "planned" not in page
    assert lint(scan(home.pages.parent, mapping_from(DEFAULT_MAPPING))) == []
    album, mina = "샘플-앨범", "미나"
    assert _edges(_memory_store) == {
        ("goal-g1", album, "ledger:G1"), (album, "goal-g1", "ledger:G1"),
        ("goal-g1", mina, "ledger:G1"), (mina, "goal-g1", "ledger:G1"),
        ("goal-g1", "mixing", "ledger:G1"), ("mixing", "goal-g1", "ledger:G1"),
    }
    assert (first["links_added_from_goals"], first["links_added_to_goals"]) == (3, 3)
    pages_before = _hashes(home.pages)

    second = _json(capsys, "projects", "sync")

    assert second["pages_created"] == second["pages_updated"] == second["pages_archived"] == []
    assert (second["links_added_from_goals"], second["links_added_to_goals"], second["links_removed"]) == (0, 0, 0)
    assert _hashes(home.pages) == pages_before
    assert _hashes(home.wiki) == wiki_before


def test_a_ledger_status_change_shows_on_the_next_read_without_a_page_edit(home, _memory_store, capsys):
    _json(capsys, "projects", "sync")
    before = _json(capsys, "projects", "show", "G1")
    assert before["status"] == "planned" and before["tasks"] == [{"id": "T1", "status": "planned"}]
    assert "Status: planned." in before["text"]
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        row = conn.execute("SELECT compiled_truth, content_sha256 FROM brain.pages WHERE slug = 'goal-g1'").fetchone()
    pages_before = _hashes(home.pages)

    home.ok("drop", "T1", "--quote", "그만")
    home.ok("drop", "G1", "--quote", "그만")
    after = _json(capsys, "projects", "show", "goal-g1")

    assert after["status"] == "dropped" and after["tasks"] == [{"id": "T1", "status": "dropped"}]
    assert "Status: dropped." in after["text"] and "T1 Mixing 마무리: dropped" in after["text"]
    assert _hashes(home.pages) == pages_before
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        assert conn.execute("SELECT compiled_truth, content_sha256 FROM brain.pages WHERE slug = 'goal-g1'"
                            ).fetchone() == row
    again = _json(capsys, "projects", "sync")
    assert again["pages_created"] == again["pages_updated"] == again["pages_archived"] == []


def test_a_goal_gone_from_the_ledger_keeps_its_page_and_links_marked_archived(home, _memory_store, capsys):
    _json(capsys, "projects", "sync")
    linked = _edges(_memory_store)
    (home.work / "work.db").unlink()
    for name in ("work.db-wal", "work.db-shm"):
        (home.work / name).unlink(missing_ok=True)
    home.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "샘플 앨범 발매", "--basis", "앨범 내자",
            "--done", "미나와 함께 발매한다", "--check", "false")

    report = _json(capsys, "projects", "sync")

    assert report["pages_archived"] == ["goal-g2"] and report["pages_updated"] == ["goal-g1"]
    assert "archived: true" in (home.pages / "goal-g2.md").read_text(encoding="utf-8")
    gone = _json(capsys, "projects", "show", "G2")
    assert gone["archived"] is True and gone["status"] == "gone from the ledger"
    # G1's task is gone with the old ledger, so its link to the page the task named goes; the rest stay.
    assert _edges(_memory_store) == linked - {("goal-g1", "mixing", "ledger:G1"), ("mixing", "goal-g1", "ledger:G1")}
    assert report["links_removed"] == 2
    assert _json(capsys, "projects", "sync")["pages_archived"] == []


def test_a_wiki_import_keeps_the_ledger_links_of_its_pages(home, _memory_store, capsys):
    _json(capsys, "projects", "sync")
    linked = _edges(_memory_store)

    code, out = _brain(capsys, "import", str(home.wiki))

    assert code == 0 and "Changes: none." in out
    assert _edges(_memory_store) == linked


def test_sync_refuses_a_work_dir_that_overlaps_an_imported_wiki(home, _memory_store, capsys):
    inside = unicodedata.normalize("NFC", str(home.wiki.resolve() / "generated"))
    home.config(extra=f"brain: {{work_dir: {inside}}}\n")
    before = _hashes(home.wiki)

    code, out = _brain(capsys, "projects", "sync")

    assert code == 1 and "overlap" in out
    assert _hashes(home.wiki) == before
    assert _edges(_memory_store) == set()
