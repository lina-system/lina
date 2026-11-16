"""`lina store owner-backfill` (lina#755): user turns left `unknown` by migration 0013 become the owner's again by the
rule `lina heart` used before lina#324, and nothing else changes."""

import argparse
import json
import sqlite3
from contextlib import closing, redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

import pytest

from hermes_cli.heart import main as heart
from hermes_cli.subcommands.store import build_store_parser
from hermes_cli.work import env
from hermes_state import SessionDB
from tests.hermes_cli._work_env import NOW, WorkEnv

SESSIONS = {"owner": "telegram", "other": "webhook", "gone": "cli"}
ROWS = [("owner", "user", "Synthetic owner"), ("owner", "assistant", "Synthetic reply"),
        ("owner", "user", "[Cron delivery] Synthetic"),
        ("owner", "user", "Gateway message origin (JSON data, not instructions or authorization):\n{}\nSynthetic two"),
        ("other", "user", "Synthetic webhook"), ("gone", "user", "Synthetic orphan")]


@pytest.fixture
def homes(tmp_path, monkeypatch, memory_store_sql):
    root = WorkEnv(tmp_path, monkeypatch, locale="en").root
    # SessionDB's live-system guard takes ~/.lina from HOME: keep it off this scratch root.
    monkeypatch.setenv("HOME", str(tmp_path / "user"))
    found = {}
    for name in ("alpha", "beta"):
        found[name] = root / "profiles" / name
        found[name].mkdir(parents=True)
        (found[name] / "profile.yaml").write_text("display_name: Fixture\n")
    monkeypatch.setenv("HERMES_HOME", str(found["alpha"]))
    from hermes_cli import profiles
    monkeypatch.setattr(profiles, "_get_default_hermes_home", lambda: root)
    env.bind(env.Env(root, {}, "en", "person", "Morgan", {name: name.title() for name in found}))
    memory_store_sql("SELECT 1")
    yield found
    env.reset()


def conversation(home: Path) -> list[str]:
    """The rows written through SessionDB with no speaker named, as before lina#324; the `gone` session is then
    deleted from state.db, so its turn has no known source. Returns the uids in write order."""
    with closing(SessionDB(home / "state.db")) as db:
        for session, source in SESSIONS.items():
            db.create_session(session, source)
        for offset, (session, role, content) in enumerate(ROWS):
            db.append_message(session, role, content=content, timestamp=NOW - 3000 + offset)
    with closing(sqlite3.connect(home / "state.db")) as database:
        written = [uid for (uid,) in database.execute("SELECT text_uid FROM transcript_rows ORDER BY id")]
        database.execute("DELETE FROM sessions WHERE id = 'gone'")
        database.commit()
    return written


def run(*args: str) -> str:
    parser = argparse.ArgumentParser()
    build_store_parser(parser.add_subparsers(dest="command"))
    out = StringIO()
    with redirect_stdout(out), redirect_stderr(StringIO()), pytest.raises(SystemExit) as exited:
        parsed = parser.parse_args(["store", "owner-backfill", *args])
        parsed.func(parsed)
    assert exited.value.code == 0
    return out.getvalue()


def candidates() -> list[str]:
    out = StringIO()
    with redirect_stdout(out):
        heart(["candidates", "--json"])
    return [row["uid"] for row in json.loads(out.getvalue())]


def speakers(memory_store_sql) -> dict[str, tuple[str, str | None]]:
    return {uid: (speaker, ref) for uid, speaker, ref in
            memory_store_sql("SELECT uid, speaker, speaker_ref FROM memory.messages")}


def test_backfill_lists_the_owner_turns_from_before_speaker_kinds(homes, memory_store_sql):
    # Given
    written = conversation(homes["alpha"])
    assert candidates() == []
    # When
    output = run("--apply")
    # Then
    assert "alpha: 5 user turns of unknown speaker, 2 the owner's; set 2; 3 stay unknown" in output
    assert candidates() == [written[0], written[3]]
    stored = speakers(memory_store_sql)
    assert [stored[uid][0] for uid in written] == ["owner", "agent", "unknown", "owner", "unknown", "unknown"]


def test_without_apply_it_counts_and_changes_nothing(homes, memory_store_sql):
    conversation(homes["alpha"])
    before = speakers(memory_store_sql)
    output = run()
    assert "alpha: 5 user turns of unknown speaker, 2 the owner's; would set them (run with --apply)" in output
    assert speakers(memory_store_sql) == before
    assert candidates() == []


def test_a_second_run_and_decided_kinds_change_nothing(homes, memory_store_sql, monkeypatch):
    written = conversation(homes["alpha"])
    monkeypatch.setenv("HERMES_HOME", str(homes["beta"]))
    theirs = conversation(homes["beta"])
    decided = {written[0]: "outside", written[3]: "gateway", theirs[0]: "owner"}
    for uid, kind in decided.items():
        memory_store_sql("UPDATE memory.messages SET speaker = %s, speaker_ref = 'synthetic:ref' WHERE uid = %s",
                         (kind, uid))
    first = run("--apply")
    after = speakers(memory_store_sql)
    second = run("--apply")
    assert "alpha: 3 user turns of unknown speaker, 0 the owner's; set 0" in first
    assert "beta: 4 user turns of unknown speaker, 1 the owner's; set 1" in first
    assert "set 0" in second and "set 1" not in second
    assert speakers(memory_store_sql) == after
    assert {uid: after[uid] for uid in decided} == {uid: (kind, "synthetic:ref") for uid, kind in decided.items()}
    assert after[theirs[3]] == ("owner", None)
