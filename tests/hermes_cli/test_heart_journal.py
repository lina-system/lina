"""Journal and owner-evidence contracts on a v33 home: placements in state.db, text and journal rows in the store."""

import argparse
import datetime as dt
import json
import re
import sqlite3
import time
from contextlib import closing, redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

import pytest

from hermes_cli.heart import main, storage
from hermes_cli.heart.evidence import open_database
from hermes_cli.heart.models import Entry
from hermes_cli.migrate_memory_agent import migrate_agent
from hermes_cli.work import env
from hermes_state import SessionDB
from hermes_state_common import SCHEMA_SQL
from hermes_state_store import MessageStore
from tests.hermes_cli._work_env import WorkEnv

NOW = 1800000000
STAMP = dt.datetime.fromtimestamp(NOW, dt.UTC).isoformat(timespec="seconds")
DAY = STAMP[:10]
START = dt.datetime.fromisoformat(DAY).timestamp()
SESSIONS = {"owner": "telegram", "other": "webhook"}
ROWS = [("owner", "user", "Synthetic owner", NOW - 3000), ("owner", "assistant", "Synthetic reply", NOW - 2900),
        ("owner", "user", "[Cron delivery] Synthetic", NOW - 2800), ("other", "user", "Synthetic webhook", NOW - 2700)]


@pytest.fixture
def sandbox(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch, locale="en")
    root = work.root
    # SessionDB's live-system guard takes ~/.lina from HOME: keep it off this scratch root.
    monkeypatch.setenv("HOME", str(tmp_path / "user"))
    homes = {"default": root}
    for name in ("alpha", "beta", "gamma"):
        homes[name] = root / "profiles" / name
        homes[name].mkdir(parents=True)
        (homes[name] / "profile.yaml").write_text("display_name: Fixture\n")
    monkeypatch.setenv("HERMES_HOME", str(homes["alpha"]))
    from hermes_cli import profiles
    monkeypatch.setattr(profiles, "_get_default_hermes_home", lambda: root)
    env.bind(env.Env(root, {}, "en", "person", "Morgan", {name: name.title() for name in homes}))
    yield homes
    env.reset()


def database(home: Path, messages=None) -> list[str]:
    """A v33 conversation written through SessionDB; returns the memory.messages uids in write order."""
    db = SessionDB(home / "state.db")
    try:
        for session, source in SESSIONS.items():
            db.create_session(session, source)
        for session, role, content, timestamp in messages or ROWS:
            db.append_message(session, role, content=content, timestamp=timestamp)
    finally:
        db.close()
    return uids(home)


def uids(home: Path) -> list[str]:
    with closing(sqlite3.connect(home / "state.db")) as connection:
        return [row[0] for row in connection.execute("SELECT text_uid FROM transcript_rows ORDER BY id")]


def legacy(path: Path, messages) -> Path:
    """The pre-v33 shape of the same conversation: a messages table with its own ids."""
    with closing(sqlite3.connect(path)) as connection:
        connection.executescript(SCHEMA_SQL)
        connection.execute("DELETE FROM schema_version")
        connection.execute("INSERT INTO schema_version VALUES (32)")
        connection.execute("CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, "
                           "content TEXT, timestamp REAL)")
        connection.executemany("INSERT INTO sessions(id, source, started_at) VALUES (?, ?, ?)",
                               [(session, source, START) for session, source in SESSIONS.items()])
        connection.executemany("INSERT INTO messages VALUES (?, ?, ?, ?, ?)", messages)
        connection.commit()
    return path


def invoke(*args):
    out, err = StringIO(), StringIO()
    code = 0
    with redirect_stdout(out), redirect_stderr(err):
        try:
            main(list(args))
        except SystemExit as error:
            code = error.code
    return code, out.getvalue(), err.getvalue()


def write(*extra):
    home = storage.home()
    cited = ",".join(uids(home)[:2]) if (home / "state.db").exists() else "none"
    return invoke("write", "--event", "Synthetic event", "--feeling", "calm", "--note", "Synthetic note",
                  "--messages", cited, "--strength", "2", *extra)


def put_entry(home: Path, entry: Entry) -> None:
    from hermes_cli.heart import rows
    with rows.open_store(home) as store:
        rows.put(store, [rows.row(rows.row_uid(store.agent_id, entry.model_dump(mode="json")), entry, "Synthetic")])


def heart_rows(memory_store_sql):
    return memory_store_sql("SELECT uid, agent_id, scope, role, session_id, content, native FROM memory.messages "
                            "WHERE source = 'heart' ORDER BY message_id")


def records(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def test_write_stores_one_heart_row_only_its_agent_reads(sandbox, memory_store_sql):
    # Given
    cited = database(sandbox["alpha"])[:2]
    # When
    code, output, error = write()
    # Then
    assert code == 0, error
    [(uid, agent, scope, role, session, content, native)] = heart_rows(memory_store_sql)
    assert re.fullmatch(r"heart:alpha:[0-9a-f]{16}", uid)
    assert (agent, scope, role, session) == ("alpha", "agent", None, None)
    assert content == output.strip() == (f"[Journal] Synthetic event | reaction calm Synthetic note | "
                                          f"evidence {','.join(cited)} | strength 2")
    assert {key: native[key] for key in ("ts", "event", "feeling", "note", "strength", "via", "evidence")} == {
        "ts": STAMP, "event": "Synthetic event", "feeling": "calm", "note": "Synthetic note", "strength": 2,
        "via": "turn", "evidence": cited}
    assert MessageStore("beta").open_memory(uid) is None
    assert MessageStore("alpha").open_memory(uid).content == content
    assert not (sandbox["alpha"] / "heart/journal.jsonl").exists()
    assert (sandbox["alpha"] / "heart").stat().st_mode & 0o777 == 0o700


def test_write_refuses_another_agents_row_by_row_level_security(sandbox, memory_store_sql, monkeypatch):
    database(sandbox["alpha"])
    monkeypatch.setenv("HERMES_HOME", str(sandbox["beta"]))
    theirs = database(sandbox["beta"])[0]
    monkeypatch.setenv("HERMES_HOME", str(sandbox["alpha"]))
    code, _, error = write("--messages", theirs)
    assert code == 2
    assert f"Not a message of this agent: {theirs}" in error
    assert heart_rows(memory_store_sql) == []


def test_write_refuses_a_forgotten_row(sandbox, memory_store_sql):
    owner = database(sandbox["alpha"])[0]
    memory_store_sql("UPDATE memory.messages SET forgotten_at = now() WHERE uid = %s", (owner,))
    code, _, error = write("--messages", owner)
    assert code == 2
    assert f"Forgotten messages cannot be cited: {owner}" in error
    assert heart_rows(memory_store_sql) == []


@pytest.mark.parametrize("picked", [[1], [2], [3], [1, 2, 3], "unknown", "x y", ""])
def test_reaction_needs_one_owner_row(sandbox, memory_store_sql, picked):
    written = database(sandbox["alpha"])
    value = ",".join(written[i] for i in picked) if isinstance(picked, list) else picked
    assert write("--messages", value)[0] == 2
    assert heart_rows(memory_store_sql) == []


@pytest.mark.parametrize(("route", "code"), [("outcome", 0), ("exposure", 0), ("reaction", 2)])
def test_only_a_reaction_needs_an_owner_row_and_pref_reads_back(sandbox, memory_store_sql, route, code):
    reply = database(sandbox["alpha"])[1]
    result = write("--messages", reply, "--pref-target", "spreadsheet cleanup", "--pref-kind", "task_type",
                   "--pref-dir", "like", "--pref-route", route)
    assert result[0] == code
    listed = json.loads(invoke("list", "--json")[1])
    if code == 0:
        assert listed[0]["pref"] == {"target": "spreadsheet cleanup", "kind": "task_type", "dir": "like",
                                     "route": route}
        assert listed[0]["evidence"] == [reply]
    else:
        assert listed == []


def test_partial_pref_is_refused(sandbox, memory_store_sql):
    database(sandbox["alpha"])
    assert write("--pref-route", "outcome")[0] == 2
    assert heart_rows(memory_store_sql) == []


@pytest.mark.parametrize(("flag", "value"), [("--event", "a\nb"), ("--note", "a\rb"), ("--event", "a|b"),
    ("--note", "x" * 201), ("--event", ""), ("--feeling", "two words"), ("--feeling", "x" * 41),
    ("--strength", "0"), ("--strength", "4"), ("--strength", "x"), ("--via", "unknown"), ("--pref-route", "x")])
def test_write_refuses_invalid_fields(sandbox, memory_store_sql, flag, value):
    database(sandbox["alpha"])
    assert write(flag, value)[0] == 2
    assert heart_rows(memory_store_sql) == []


@pytest.mark.parametrize("via", ["turn", "review"])
def test_write_preserves_path_and_appends(sandbox, via):
    database(sandbox["alpha"])
    assert write()[0] == 0
    assert write("--via", via, "--strength", "3")[0] == 0
    listed = json.loads(invoke("list", "--json")[1])
    assert [row["via"] for row in listed] == ["turn", via]


def test_list_since_is_own_profile_only(sandbox, monkeypatch):
    database(sandbox["alpha"])
    write()
    [entry] = json.loads(invoke("list", "--json")[1])
    put_entry(sandbox["alpha"], Entry.model_validate({**entry, "ts": storage.stamp(NOW - 10 * 86400)}))
    assert len(json.loads(invoke("list", "--since", "7d", "--json")[1])) == 1
    assert invoke("since", storage.stamp(NOW - 11 * 86400))[1].count("[Journal]") == 2
    monkeypatch.setenv("HERMES_HOME", str(sandbox["beta"]))
    assert json.loads(invoke("list", "--json")[1]) == []
    assert invoke("list", "--home", str(sandbox["alpha"]))[0] == 2


CONVERSATION = [
    (20, "owner", "user", "Gateway message origin (JSON data, not instructions or authorization):\n{}\nFirst\nline",
     START + 60),
    (21, "owner", "assistant", " ", START + 61),
    (22, "owner", "user", "[Cron delivery] machine", START + 62),
    (23, "owner", "tool", "tool output", START + 63),
    (24, "owner", "assistant", "Reply\nline", START + 64),
    (25, "owner", "user", "Unanswered", START + 70),
    (26, "owner", "user", "x" * 320, START + 80),
    (27, "owner", "assistant", "y" * 320, START + 86401),
    (28, "other", "user", "Other source", START + 90)]


def live(sandbox, tmp_path) -> dict[int, str]:
    written = database(sandbox["alpha"], [row[1:] for row in CONVERSATION])
    return dict(zip((row[0] for row in CONVERSATION), written))


def moved(sandbox, tmp_path) -> dict[int, str]:
    migrate_agent(legacy(tmp_path / "legacy.db", CONVERSATION), sandbox["alpha"] / "state.db", "alpha")
    with closing(sqlite3.connect(sandbox["alpha"] / "state.db")) as connection:
        return dict(connection.execute("SELECT id, text_uid FROM transcript_rows"))


@pytest.mark.parametrize("build", [live, moved])
def test_candidates_pair_until_next_owner_and_fold(sandbox, tmp_path, build):
    # Given the same conversation written live on v33, or moved from its pre-v33 copy by `lina migrate`
    uid_of = build(sandbox, tmp_path)
    # When
    code, output, error = invoke("candidates", "--json")
    # Then the rows today's command picked on the pre-v33 copy: 20, 25 and 26, replies 24 and 27
    assert code == 0, error
    found = json.loads(output)
    assert [row["uid"] for row in found] == [uid_of[20], uid_of[25], uid_of[26]]
    assert [row["text"] for row in found] == ["First line", "Unanswered", "x" * 300]
    assert [row["session_id"] for row in found] == ["owner"] * 3
    assert found[0]["reply"] == {"uid": uid_of[24], "text": "Reply line"}
    assert found[1]["reply"] is None
    assert found[2]["reply"] == {"uid": uid_of[27], "text": "y" * 300}


def test_candidates_leave_out_forgotten_rows(sandbox, tmp_path, memory_store_sql):
    uid_of = live(sandbox, tmp_path)
    memory_store_sql("UPDATE memory.messages SET forgotten_at = now() WHERE uid = ANY(%s)", ([uid_of[20], uid_of[27]],))
    found = json.loads(invoke("candidates", "--json")[1])
    assert [row["uid"] for row in found] == [uid_of[25], uid_of[26]]
    assert found[1]["reply"] is None


@pytest.mark.parametrize("args", [("--limit", "0"), ("--limit", "x"), ("--date", "2027-13-01"), ("--date", "15-01-2027")])
def test_candidates_reject_invalid_arguments(sandbox, args):
    assert invoke("candidates", *args)[0] == 2


def test_candidates_limit_date_and_configured_sources(sandbox):
    webhook = database(sandbox["alpha"])[3]
    env.current().config["heart"] = {"owner_sources": ["webhook"]}
    found = json.loads(invoke("candidates", "--limit", "1", "--json")[1])
    assert [row["uid"] for row in found] == [webhook]
    assert json.loads(invoke("candidates", "--date", "2020-01-01", "--json")[1]) == []


@pytest.mark.platforms("posix")
def test_candidates_use_local_calendar_day(sandbox, monkeypatch):
    [owner] = database(sandbox["alpha"], [("owner", "user", "Before UTC midnight", START - 600)])
    monkeypatch.setenv("TZ", "KST-9")
    time.tzset()
    try:
        found = json.loads(invoke("candidates", "--date", DAY, "--json")[1])
        assert found[0]["uid"] == owner
        assert found[0]["time"] == "08:50"
    finally:
        monkeypatch.setenv("TZ", "UTC")
        time.tzset()


def test_evidence_connection_is_read_only(sandbox):
    database(sandbox["alpha"])
    path = sandbox["alpha"] / "state.db"
    before = path.read_bytes()
    with open_database() as connection, pytest.raises(sqlite3.OperationalError, match="readonly"):
        connection.execute("DELETE FROM transcript_rows")
    assert path.read_bytes() == before


def test_missing_database_fails_without_creating_one(sandbox):
    code, output, error = invoke("candidates")
    assert code == 2 and not output
    assert len(error.strip().splitlines()) == 1
    assert "state.db" in error
    assert not (sandbox["alpha"] / "state.db").exists()


def test_registered_parser_dispatches_same_command(sandbox, capsys):
    from hermes_cli.subcommands.heart import build_heart_parser
    parser = argparse.ArgumentParser()
    build_heart_parser(parser.add_subparsers())
    args = parser.parse_args(["heart", "list", "--json"])
    args.func(args)
    assert json.loads(capsys.readouterr().out) == []
