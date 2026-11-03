"""Kanban links retain board identity and reject malformed cards (lina#36)."""

import pytest

import sqlite3
from contextlib import closing

from tests.hermes_cli._work_env import WorkEnv


@pytest.mark.parametrize("link", ["kanban:t_1234abcd", "kanban:team/t_1234abcd"])
def test_card_link_roundtrips_when_well_formed(tmp_path, monkeypatch, link):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    work.ok("link", "T1", link)
    # Then
    row = next(row for row in work.json()["items"] if row["id"] == "T1")
    assert link in row["links"]


@pytest.mark.parametrize("link", ["kanban:t_short", "kanban:team/", "kanban:bad board/t_1234abcd"])
def test_card_link_refuses_when_malformed(tmp_path, monkeypatch, link):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    code, _, _ = work.run("link", "T1", link)
    # Then
    assert code == 2


@pytest.mark.parametrize("board,status", [("", "running"), ("team", "done")])
def test_card_reads_its_own_board_database(tmp_path, monkeypatch, board, status):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli import kanban_db
    from hermes_cli.work import links
    path = tmp_path / "cards.db"
    with closing(sqlite3.connect(path)) as db:
        db.execute("CREATE TABLE tasks (id TEXT, status TEXT)")
        db.execute("INSERT INTO tasks VALUES (?, ?)", ("t_1234abcd", status))
        db.commit()
    seen = []

    def database_path(*, board=None):
        seen.append(board)
        return path

    monkeypatch.setattr(kanban_db, "kanban_db_path", database_path)
    links._KANBAN_CACHE.clear()
    link = f"kanban:{board + '/' if board else ''}t_1234abcd"
    # When
    actual = links.kanban_read(link)
    # Then
    assert actual == status
    assert seen == [board or None]


def test_missing_card_is_unknown_when_database_does_not_exist(tmp_path, monkeypatch):
    # Given
    WorkEnv(tmp_path, monkeypatch)
    from hermes_cli import kanban_db
    from hermes_cli.work import links
    monkeypatch.setattr(kanban_db, "kanban_db_path", lambda **kwargs: tmp_path / "missing.db")
    links._KANBAN_CACHE.clear()
    # When
    actual = links.kanban_read("kanban:t_1234abcd")
    # Then
    assert actual == ""
