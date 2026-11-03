"""Real command surface for a new agent beside untouched synthetic Hermes profiles."""

from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import sys
from contextlib import closing

from tests.hermes_cli.migrate_memory_fixtures import legacy_home, spool_fixture


def _command(tmp_path, store):
    source = legacy_home(tmp_path / "hermes")
    spool = spool_fixture(tmp_path / "spool")
    args = ["migrate", "memory", "--from", str(source), "--source-profile", "alpha", "--as", "alpha-copy",
            "--home", str(tmp_path / "lina"), "--work-dir", str(tmp_path / "copy"), "--spool", str(spool)]
    # Same main() as the installed lina entrypoint, with only the ephemeral DB resolver substituted.
    script = ("import sys; import hermes_state_store as s; dsn=sys.argv.pop(1); "
              "s.resolve_owner_dsn=lambda:dsn; from hermes_cli.main import main; main()")
    return [sys.executable, "-c", script, store.superuser_dsn(), *args]


def _run(command, tmp_path):
    return subprocess.run(command, capture_output=True, text=True, timeout=90, check=False,
                          env={**os.environ, "LINA_HOME": str(tmp_path / "lina"),
                               "HERMES_HOME": str(tmp_path / "lina")})


def test_real_cli_migrates_one_profile_and_replays_without_inserts(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    source = tmp_path / "hermes" / "profiles" / "alpha" / "state.db"
    before = source.read_bytes()

    done = _run(command, tmp_path)

    assert done.returncode == 0, done.stdout + done.stderr
    report = json.loads(done.stdout)
    assert report["ok"] and report["lcm"]["counted"] is False
    assert report["sources"][0]["source_rows"] == 5
    assert memory_store_sql("SELECT DISTINCT agent_id FROM memory.messages WHERE source='agent'") == [("alpha-copy",)]
    assert source.read_bytes() == before
    assert (tmp_path / "lina/profiles/alpha-copy/SOUL.md").read_text() == "Synthetic alpha persona\n"
    assert (tmp_path / "lina/profiles/alpha-copy/memories/MEMORY.md").read_text() == "Synthetic alpha memory\n"
    again = _run(command, tmp_path)
    assert again.returncode == 0, again.stdout + again.stderr
    second = json.loads(again.stdout)
    assert all(row["inserted"] == 0 for row in second["sources"])
    assert [r["source_manifest"] for r in second["sources"]] == [r["source_manifest"] for r in report["sources"]]
    assert json.loads((tmp_path / "copy/migration-report.json").read_text()) == second


def test_real_cli_missing_fixture_row_exits_nonzero(tmp_path, _memory_store):
    command = _command(tmp_path, _memory_store)
    done = _run(command, tmp_path)
    assert done.returncode == 0, done.stdout + done.stderr
    with closing(sqlite3.connect(tmp_path / "lina/profiles/alpha-copy/state.db")) as conn:
        conn.execute("DELETE FROM transcript_rows WHERE id=5")
        conn.commit()

    failed = _run(command, tmp_path)

    assert failed.returncode != 0
    report = json.loads(failed.stdout)
    assert report["ok"] is False
    assert "session-a:5" in report["error"]


def test_real_cli_snapshot_rerun_after_live_ingest(tmp_path, _memory_store, memory_store_sql):
    # Given: a real migration followed by the normal live ingestion command.
    from tests.hermes_cli.test_external_messages import event, message

    command = _command(tmp_path, _memory_store)
    first = _run(command, tmp_path)
    assert first.returncode == 0, first.stdout + first.stderr
    original = tmp_path / "spool/events.jsonl"
    with original.open("ab") as stream:
        stream.write(event(message(native="new-live-row"), number=42))
    live = command[:command.index("migrate")] + ["memory", "external-messages", "ingest", "--spool", str(original)]
    ingested = _run(live, tmp_path)
    assert ingested.returncode == 0, ingested.stdout + ingested.stderr
    cursor = memory_store_sql("SELECT * FROM memory.external_message_cursors")

    # When: the exact original migration command is repeated.
    repeated = _run(command, tmp_path)

    # Then: copied rows match and the advanced live cursor is untouched.
    assert repeated.returncode == 0, repeated.stdout + repeated.stderr
    external = next(row for row in json.loads(repeated.stdout)["sources"] if row["source"] == "external")
    assert external["inserted"] == 0 and external["matched"] == external["source_rows"] == 4
    assert memory_store_sql("SELECT * FROM memory.external_message_cursors") == cursor


def test_existing_configuration_migration_subcommands_still_dispatch():
    import argparse

    from hermes_cli.migrate import cmd_migrate_xai
    from hermes_cli.subcommands.migrate import _cmd_migrate_relay, build_migrate_parser

    parser = argparse.ArgumentParser()
    build_migrate_parser(parser.add_subparsers())
    assert parser.parse_args(["migrate", "xai"]).func is cmd_migrate_xai
    assert parser.parse_args(["migrate", "relay"]).func is _cmd_migrate_relay


def test_real_cli_copies_all_profiles_sequentially(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    index = command.index("--source-profile")
    command[index:index + 2] = ["--all-source-profiles"]
    command[command.index("--as") + 1] = "copied"

    done = _run(command, tmp_path)

    assert done.returncode == 0, done.stdout + done.stderr
    report = json.loads(done.stdout)
    assert [row["agent"] for row in report["profiles"]] == ["copied-alpha", "copied-beta"]
    assert memory_store_sql("SELECT DISTINCT agent_id FROM memory.messages WHERE source='agent' ORDER BY 1") == [
        ("copied-alpha",), ("copied-beta",)]
    repeated = _run(command, tmp_path)
    assert repeated.returncode == 0, repeated.stdout + repeated.stderr
    assert all(source["inserted"] == 0 for profile in json.loads(repeated.stdout)["profiles"]
               for source in profile["sources"])


def test_global_profile_keeps_runtime_meaning(tmp_path, _memory_store):
    command = _command(tmp_path, _memory_store)
    runtime = tmp_path / "lina/profiles/runtime"
    runtime.mkdir(parents=True)
    (runtime / "SOUL.md").write_text("Synthetic runtime profile\n")
    command[command.index("migrate"):command.index("migrate")] = ["--profile", "runtime"]

    done = _run(command, tmp_path)

    assert done.returncode == 0, done.stdout + done.stderr
    report = json.loads(done.stdout)
    assert report["profile"] == "alpha" and report["agent"] == "alpha-copy"
    assert not (runtime / "state.db").exists()


def test_destination_profile_symlink_cannot_redirect_writes(tmp_path, _memory_store):
    command = _command(tmp_path, _memory_store)
    home = tmp_path / "lina"
    home.mkdir()
    other = tmp_path / "unrelated"
    other.mkdir()
    (home / "profiles").symlink_to(other, target_is_directory=True)

    done = _run(command, tmp_path)

    assert done.returncode != 0
    assert list(other.iterdir()) == []


def test_default_destination_is_running_lina_home(tmp_path, _memory_store):
    command = _command(tmp_path, _memory_store)
    index = command.index("--home")
    del command[index:index + 2]

    done = _run(command, tmp_path)

    assert done.returncode == 0, done.stdout + done.stderr
    assert json.loads(done.stdout)["destination_home"] == str(tmp_path / "lina/profiles/alpha-copy")


def test_different_destination_home_refused_before_copy_or_store_write(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    other = tmp_path / "different-lina"
    command[command.index("--home") + 1] = str(other)

    done = _run(command, tmp_path)

    assert done.returncode != 0
    assert not other.exists()
    assert not (tmp_path / "copy").exists()
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]


def test_missing_honcho_host_config_refused_before_agent_rows(tmp_path, _memory_store, memory_store_sql):
    command = _command(tmp_path, _memory_store)
    dump = tmp_path / "old.dump"
    dump.write_bytes(b"synthetic archive; preflight must not reach restore")
    command.extend(["--honcho-dump", str(dump)])

    done = _run(command, tmp_path)

    assert done.returncode != 0
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]
    assert not (tmp_path / "lina/profiles/alpha-copy").exists()
