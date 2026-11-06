"""Real shared-ledger CLI batches, sequential orders and interrupted retries."""

from contextlib import closing
import json
import os
import subprocess
import sys

import pytest

from hermes_cli.config import atomic_config_write
from hermes_cli.work import env, store
from hermes_cli.migrate_memory_work import _destination_db
from hermes_cli.migrate_memory_work_rows import canonical, remap_owner, rows
from hermes_cli.work import store_sqlite
from tests.hermes_cli.migrate_memory_fixtures import legacy_home
from tests.hermes_cli.test_migrate_memory_work_batch import isolated_mapping_receipt, shared_source
from tests.hermes_cli.test_migrate_memory_work_ancestry import target
from tests.hermes_cli.work_pg_fixture import pg_db

__all__ = ["isolated_mapping_receipt", "pg_db", "target"]


@pytest.mark.parametrize("mode", ["all", "alpha-first", "beta-first", "retry"])
def test_shared_work_cli_matches_across_orders_and_retry(target, tmp_path, _memory_store, mode):
    source = legacy_home(tmp_path / "source")
    shared = shared_source(tmp_path / "shared.db")
    atomic_config_write(target / "config.yaml", env.current().config)
    entry = ("import sys; import hermes_state_store as s; "
             "dsn=sys.stdin.readline().strip(); s.resolve_owner_dsn=lambda:dsn; "
             "import hermes_cli.migrate_memory as m; original=m._profile; "
             "fail=sys.argv.pop(1)=='fail'; "
             "m._profile=lambda a: (_ for _ in ()).throw(ValueError('injected second profile failure')) "
             "if fail and a.profile=='beta' else original(a); "
             "from hermes_cli.main import main; main()")
    common = ["migrate", "memory", "--from", str(source), "--work-db", str(shared)]
    names = {"alpha": "copied-alpha", "beta": "copied-beta"}
    commands = ([common + ["--all-source-profiles", "--as", "copied", "--work-dir", str(tmp_path / "copy")]]
                if mode in {"all", "retry"} else
                [common + ["--source-profile", profile, "--as", names[profile], "--work-dir", str(tmp_path / profile)]
                 for profile in (("alpha", "beta") if mode == "alpha-first" else ("beta", "alpha"))])

    def run(command, fail=False):
        return subprocess.run([sys.executable, "-c", entry, "fail" if fail else "pass", *command],
                              input=_memory_store.superuser_dsn() + "\n", text=True, capture_output=True,
                              env={**os.environ, "LINA_HOME": str(target), "HERMES_HOME": str(target)},
                              timeout=120, check=False)

    if mode == "retry":
        failed = run(commands[0], True)
        assert failed.returncode == 1 and "injected second profile failure" in failed.stdout
        with closing(_destination_db()) as db:
            assert db.execute("SELECT agent_id FROM area WHERE id='A1'").fetchone() == ("copied-beta",)
    for command in commands:
        done = run(command)
        assert done.returncode == 0, done.stdout + done.stderr
    with closing(_destination_db()) as db:
        assert db.execute("SELECT id,agent_id FROM todo ORDER BY id").fetchall() == [
            ("T1", "copied-alpha"), ("T2", "copied-beta")]
        assert db.execute("SELECT agent_id FROM goal").fetchall() == [("copied-beta",)]
        with closing(store_sqlite.connect(shared, "owner", {})) as original:
            expected = rows(original)
        for kind, records in rows(db).items():
            assert [canonical(db, kind, row) for row in records] == [
                canonical(db, kind, remap_owner(kind, row, names)) for row in expected[kind]]
        before = store.dump(db)
    for command in commands:
        done = run(command)
        assert done.returncode == 0, done.stdout + done.stderr
        report = json.loads(done.stdout)
        profiles = report.get("profiles", [report])
        assert all(profile["work"]["inserted"] == 0 for profile in profiles)
    with closing(_destination_db()) as db:
        assert store.dump(db) == before
