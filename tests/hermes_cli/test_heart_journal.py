"""Native state.db evidence and own-profile journal contracts (ported from heart)."""

import argparse
import datetime as dt
import json
import sqlite3
import time
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

import pytest

from hermes_cli.heart import main, storage
from hermes_cli.heart.evidence import open_database
from hermes_cli.heart.models import Entry
from hermes_cli.work import env
from tests.hermes_cli._work_env import WorkEnv

NOW = 1800000000
STAMP = dt.datetime.fromtimestamp(NOW, dt.UTC).isoformat(timespec="seconds")
DAY = STAMP[:10]
START = dt.datetime.fromisoformat(DAY).timestamp()


@pytest.fixture
def sandbox(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch, locale="en")
    root = work.root
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


def database(home: Path, rows=None) -> Path:
    path = home / "state.db"
    with sqlite3.connect(path) as connection:
        connection.executescript("CREATE TABLE sessions(id TEXT PRIMARY KEY, source TEXT);"
                                 "CREATE TABLE messages(id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, "
                                 "content TEXT, timestamp REAL);")
        connection.executemany("INSERT INTO sessions VALUES (?, ?)", [("owner", "telegram"), ("other", "webhook")])
        connection.executemany("INSERT INTO messages VALUES (?, ?, ?, ?, ?)", rows or [
            (11, "owner", "user", "Synthetic owner", NOW - 3000),
            (12, "owner", "assistant", "Synthetic reply", NOW - 2900),
            (13, "owner", "user", "[Cron delivery] Synthetic", NOW - 2800),
            (14, "other", "user", "Synthetic webhook", NOW - 2700)])
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
    return invoke("write", "--event", "Synthetic event", "--feeling", "calm", "--note", "Synthetic note",
                  "--messages", "11,12", "--strength", "2", *extra)


def records(path):
    return [json.loads(line) for line in path.read_text().splitlines()]


def test_write_appends_private_evidence_without_database_mutation(sandbox):
    # Given
    path = database(sandbox["alpha"])
    before = path.read_bytes()
    # When
    code, output, _ = write()
    # Then
    assert code == 0
    entry = records(sandbox["alpha"] / "heart/journal.jsonl")[0]
    assert entry["evidence"] == [{"message_id": 11, "session_id": "owner"}, {"message_id": 12, "session_id": "owner"}]
    assert entry["ts"] == STAMP
    assert "state:11,12" in output
    assert path.read_bytes() == before
    assert (sandbox["alpha"] / "heart").stat().st_mode & 0o777 == 0o700
    assert (sandbox["alpha"] / "heart/journal.jsonl").stat().st_mode & 0o777 == 0o600


@pytest.mark.parametrize("ids", ["99", "11,99", "12", "13", "14", "12,13,14", "x", ""])
def test_write_refuses_invalid_evidence(sandbox, ids):
    database(sandbox["alpha"])
    result = write("--messages", ids)
    assert result[0] == 2
    assert not (sandbox["alpha"] / "heart/journal.jsonl").exists()


@pytest.mark.parametrize(("flag", "value"), [("--event", "a\nb"), ("--note", "a\rb"), ("--event", "a|b"),
    ("--note", "x" * 201), ("--event", ""), ("--feeling", "two words"), ("--feeling", "x" * 41),
    ("--strength", "0"), ("--strength", "4"), ("--strength", "x"), ("--via", "unknown")])
def test_write_refuses_invalid_fields(sandbox, flag, value):
    database(sandbox["alpha"])
    assert write(flag, value)[0] == 2
    assert not (sandbox["alpha"] / "heart/journal.jsonl").exists()


@pytest.mark.parametrize("via", ["turn", "review"])
def test_write_preserves_path_and_appends(sandbox, via):
    database(sandbox["alpha"])
    assert write()[0] == 0
    assert write("--via", via)[0] == 0
    rows = json.loads(invoke("list", "--json")[1])
    assert [row["via"] for row in rows] == ["turn", via]


def test_list_since_is_own_profile_only(sandbox, monkeypatch):
    database(sandbox["alpha"])
    write()
    path = sandbox["alpha"] / "heart/journal.jsonl"
    row = records(path)[0]
    storage.append(path, Entry.model_validate({**row, "ts": storage.stamp(NOW - 10 * 86400)}))
    assert len(json.loads(invoke("list", "--since", "7d", "--json")[1])) == 1
    assert invoke("since", storage.stamp(NOW - 11 * 86400))[1].count("state:") == 2
    monkeypatch.setenv("HERMES_HOME", str(sandbox["beta"]))
    assert json.loads(invoke("list", "--json")[1]) == []
    assert invoke("list", "--home", str(sandbox["alpha"]))[0] == 2


def test_candidates_pair_until_next_owner_and_fold(sandbox):
    database(sandbox["alpha"], [
        (20, "owner", "user", "Gateway message origin (JSON data, not instructions or authorization):\n{}\nFirst\nline", START + 60),
        (21, "owner", "assistant", " ", START + 61),
        (22, "owner", "user", "[Cron delivery] machine", START + 62),
        (23, "owner", "tool", "tool output", START + 63),
        (24, "owner", "assistant", "Reply\nline", START + 64),
        (25, "owner", "user", "Unanswered", START + 70),
        (26, "owner", "user", "x" * 320, START + 80),
        (27, "owner", "assistant", "y" * 320, START + 86401),
        (28, "other", "user", "Other source", START + 90)])
    rows = json.loads(invoke("candidates", "--json")[1])
    assert [row["message_id"] for row in rows] == [20, 25, 26]
    assert rows[0]["text"] == "First line"
    assert rows[0]["reply"] == {"message_id": 24, "text": "Reply line"}
    assert rows[1]["reply"] is None
    assert rows[2]["reply"]["text"] == "y" * 300


@pytest.mark.parametrize("args", [("--limit", "0"), ("--limit", "x"), ("--date", "2027-13-01"), ("--date", "15-01-2027")])
def test_candidates_reject_invalid_arguments(sandbox, args):
    assert invoke("candidates", *args)[0] == 2


def test_candidates_limit_date_and_configured_sources(sandbox):
    database(sandbox["alpha"])
    env.current().config["heart"] = {"owner_sources": ["webhook"]}
    rows = json.loads(invoke("candidates", "--limit", "1", "--json")[1])
    assert [row["message_id"] for row in rows] == [14]
    assert json.loads(invoke("candidates", "--date", "2020-01-01", "--json")[1]) == []


@pytest.mark.platforms("posix")
def test_candidates_use_local_calendar_day(sandbox, monkeypatch):
    database(sandbox["alpha"], [(20, "owner", "user", "Before UTC midnight", START - 600)])
    monkeypatch.setenv("TZ", "KST-9")
    time.tzset()
    try:
        rows = json.loads(invoke("candidates", "--date", DAY, "--json")[1])
        assert rows[0]["message_id"] == 20
        assert rows[0]["time"] == "08:50"
    finally:
        monkeypatch.setenv("TZ", "UTC")
        time.tzset()


def test_evidence_connection_is_read_only(sandbox):
    path = database(sandbox["alpha"])
    before = path.read_bytes()
    with open_database() as connection, pytest.raises(sqlite3.OperationalError, match="readonly"):
        connection.execute("DELETE FROM messages")
    assert path.read_bytes() == before
    assert not Path(str(path) + "-journal").exists()


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
