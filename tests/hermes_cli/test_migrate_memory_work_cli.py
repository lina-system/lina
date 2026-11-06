"""The migrated agent can read its own work records through the actual CLI."""

import json
import os
import sqlite3
import subprocess
import sys
from contextlib import closing

import pytest

from hermes_cli.config import atomic_config_write
from tests.hermes_cli.migrate_memory_fixtures import legacy_home
from tests.hermes_cli.test_migrate_memory_work import build_copied_work_fixture


@pytest.mark.parametrize("foreign_closing_run", [False, True])
def test_real_migration_imports_selected_todos_and_ancestors(tmp_path, _memory_store, foreign_closing_run):
    source = legacy_home(tmp_path / "source")
    ledger = build_copied_work_fixture(tmp_path / "work.db")
    if foreign_closing_run:
        with closing(sqlite3.connect(ledger)) as db:
            db.execute("UPDATE todo SET close_run_id='run-beta' WHERE id='T1'")
            db.commit()
    home = tmp_path / "lina"
    home.mkdir()
    atomic_config_write(home / "config.yaml", {
        "work": {"store": {"backend": "sqlite"}, "agents": {"alpha-copy": "Copied agent"}}})
    script = ("import sys; import hermes_state_store as s; dsn=sys.argv.pop(1); "
              "s.resolve_owner_dsn=lambda:dsn; from hermes_cli.main import main; main()")
    env = {**os.environ, "LINA_HOME": str(home), "HERMES_HOME": str(home)}
    command = [sys.executable, "-c", script, _memory_store.superuser_dsn(), "migrate", "memory",
               "--from", str(source), "--source-profile", "alpha", "--as", "alpha-copy",
               "--work-dir", str(tmp_path / "copy"), "--work-db", str(ledger)]

    completed = subprocess.run(command, env=env, capture_output=True, text=True, timeout=120, check=False)

    assert completed.returncode == 0, completed.stdout + completed.stderr
    report = json.loads(completed.stdout)
    assert report["work"]["ok"] and report["work"]["status"] == "imported"
    assert report["work"]["source_rows"] == report["work"]["matched"] + report["work"]["inserted"]
    if foreign_closing_run:
        assert {"id": "run-beta", "kind": "run", "profile": "beta"} in report["work"]["required_ancestors"]
        with closing(sqlite3.connect(home / "work/work.db")) as db:
            assert db.execute("SELECT agent_id FROM run WHERE id='run-beta'").fetchone() == ("beta",)
    readback = subprocess.run([sys.executable, "-c", "from hermes_cli.main import main; main()",
                               "work", "json", "--all"], env=env, capture_output=True, text=True,
                              timeout=60, check=False)
    assert readback.returncode == 0, readback.stdout + readback.stderr
    items = {item["id"]: item for item in json.loads(readback.stdout)["items"]}
    assert items["T1"]["by"] == "alpha-copy"
    assert items["G1"]["by"] == "beta" and items["A1"]["by"] == "beta"
    assert "T2" not in items
