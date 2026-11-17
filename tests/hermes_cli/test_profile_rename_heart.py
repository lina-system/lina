"""Regression for #750: a renamed profile keeps its journal and usable evidence."""

import json
from contextlib import closing, redirect_stdout
from io import StringIO
from pathlib import Path
from unittest.mock import patch

import pytest

from hermes_cli.heart import main
from hermes_cli.heart import rows
from hermes_cli.profiles import create_profile, rename_profile
from hermes_cli.work import env
from hermes_state import SessionDB
from hermes_state_store import MessageStore, TEXT_CACHE


@pytest.fixture
def journal_profiles(tmp_path, monkeypatch):
    root = tmp_path / ".hermes"
    root.mkdir()
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HERMES_HOME", str(root))
    monkeypatch.setenv("LINA_HOME", str(root))
    env.bind(env.Env(root, {}, "en", "owner", "Owner", {}))
    yield root
    env.reset()


def _heart(*args: str) -> str:
    output = StringIO()
    with redirect_stdout(output):
        main(list(args))
    return output.getvalue()


def _seed_journal(name: str, monkeypatch) -> tuple[Path, list[str], str]:
    home = create_profile(name, no_alias=True)
    monkeypatch.setenv("HERMES_HOME", str(home))
    with closing(SessionDB(home / "state.db")) as db:
        db.create_session("journal", "cli")
        db.append_message("journal", "user", content="The synthetic logbook is ready.")
        db.append_message("journal", "assistant", content="I checked the synthetic logbook.")
        evidence = [row[0] for row in db._conn.execute(
            "SELECT text_uid FROM transcript_rows ORDER BY id")]
    with closing(MessageStore(name)) as store:
        store.set_speakers([(evidence[0], "owner", None)])
    _heart("write", "--event", "Checked the logbook", "--feeling", "calm",
           "--note", "The evidence is available", "--messages", ",".join(evidence), "--strength", "2")
    day = json.loads(_heart("list", "--json"))[0]["ts"][:10]
    return home, evidence, day


def _stored(memory_store_sql) -> list:
    return memory_store_sql(
        "SELECT uid, agent_id, source, content, native, speaker_ref FROM memory.messages ORDER BY message_id")


def test_renamed_profile_lists_journal_and_daily_review_with_usable_evidence(
        journal_profiles, monkeypatch, memory_store_sql):
    _seed_journal("neighbor", monkeypatch)
    sibling = [row for row in _stored(memory_store_sql) if row[1] == "neighbor"]
    old_home, evidence, day = _seed_journal("harbor", monkeypatch)
    expected = [uid.replace("agent:harbor:", "agent:lighthouse:", 1) for uid in evidence]
    monkeypatch.setenv("HERMES_HOME", str(journal_profiles))

    with patch("hermes_cli.profiles.check_alias_collision", return_value="skip"):
        new_home = rename_profile("harbor", "lighthouse")

    monkeypatch.setenv("HERMES_HOME", str(new_home))
    listed = json.loads(_heart("list", "--json"))
    assert len(listed) == 1
    assert listed[0]["evidence"] == expected
    review = _heart("review-input", "--date", day)
    assert "Checked the logbook" in review
    assert ",".join(expected) in review
    with rows.open_store(new_home) as store:
        assert rows.texts(store, expected) == {
            expected[0]: "The synthetic logbook is ready.",
            expected[1]: "I checked the synthetic logbook.",
        }
    TEXT_CACHE.clear()
    with closing(SessionDB(new_home / "state.db")) as db:
        assert [message["content"] for message in db.get_messages_as_conversation("journal")] == [
            "The synthetic logbook is ready.", "I checked the synthetic logbook."]
    stored = _stored(memory_store_sql)
    assert [row for row in stored if row[1] == "neighbor"] == sibling
    assert not any(row[1] == "harbor" for row in stored)
    assert next(row[0] for row in stored if row[1] == "lighthouse" and row[2] == "heart").startswith(
        "heart:lighthouse:")
    assert not old_home.exists()


def test_directory_rename_failure_restores_journal_and_evidence(
        journal_profiles, monkeypatch, memory_store_sql):
    old_home, evidence, day = _seed_journal("harbor", monkeypatch)
    before = _stored(memory_store_sql)
    real_rename = Path.rename
    moved_evidence = [uid.replace("agent:harbor:", "agent:lighthouse:", 1) for uid in evidence]

    def fail_directory_move(path, target):
        if path != old_home:
            return real_rename(path, target)
        with rows.open_store(target) as store:
            assert [entry.evidence for entry in rows.entries(store)] == [tuple(moved_evidence)]
            assert set(rows.texts(store, moved_evidence)) == set(moved_evidence)
        raise OSError("synthetic directory move failed")

    monkeypatch.setattr(Path, "rename", fail_directory_move)
    with pytest.raises(OSError, match="synthetic directory move failed"):
        rename_profile("harbor", "lighthouse")

    assert _stored(memory_store_sql) == before
    assert old_home.is_dir()
    assert not old_home.with_name("lighthouse").exists()
    assert json.loads(_heart("list", "--json"))[0]["evidence"] == evidence
    assert ",".join(evidence) in _heart("review-input", "--date", day)
