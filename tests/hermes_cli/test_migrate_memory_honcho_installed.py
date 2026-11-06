"""Run the installed Compose job against an isolated store without a Python override."""

from functools import partial
import json
import os
import subprocess
import sys
import uuid

import psycopg
import pytest

from hermes_cli.migrate_memory_honcho import migrate_honcho
from hermes_cli.migrate_memory_honcho_stage import upgrade
from tests._fixtures.memory_store import IMAGE, bootstrap
from tests.hermes_cli._migrate_memory_honcho_fixture import (
    create_dump, honcho_python, pg_command,
)
from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_runtime

__all__ = ["honcho_runtime"]


def test_installed_compose_import_without_honcho_python(tmp_path, monkeypatch, honcho_runtime):
    from hermes_cli import store

    ready = subprocess.run(["docker", "compose", "version"], capture_output=True, check=False)
    if ready.returncode:
        reason = "installed Honcho migration test needs Docker Compose"
        if os.environ.get("CI", "").lower() == "true":
            pytest.fail(reason)
        pytest.skip(reason)
    honcho_python()
    project = "honcho-import-test-" + uuid.uuid4().hex[:12]
    compose = tmp_path / "compose.json"
    compose.write_text(json.dumps({"services": {
        "db": {"image": IMAGE, "environment": {"POSTGRES_USER": "lina", "POSTGRES_PASSWORD": "fixture", "POSTGRES_DB": "postgres"},
               "ports": ["127.0.0.1::5432"], "tmpfs": ["/var/lib/postgresql/data"],
               "healthcheck": {"test": ["CMD", "pg_isready", "-U", "lina"], "interval": "1s", "timeout": "5s", "retries": 30}},
        "honcho-migrate": {"image": store.HONCHO_IMAGE, "profiles": ["job"]}}}))
    envfile = tmp_path / "store.env"
    envfile.write_text("")
    monkeypatch.setattr(store, "COMPOSE_FILE", compose)
    monkeypatch.setattr(store, "Store", partial(store.Store, project=project))
    monkeypatch.setattr(store, "store_dir", lambda: tmp_path)
    installed = store.Store(tmp_path)
    try:
        subprocess.run(installed.compose_argv("up", "-d", "--wait", "--wait-timeout", "60", "db"),
                       capture_output=True, check=True, timeout=90)
        port = subprocess.run(installed.compose_argv("port", "db", "5432"), capture_output=True,
                              text=True, check=True).stdout.strip().rsplit(":", 1)[1]
        admin = f"host=127.0.0.1 port={port} user=lina password=fixture dbname=postgres"
        bootstrap(admin)
        target = admin.replace("dbname=postgres", "dbname=lina")
        monkeypatch.setattr("hermes_state_store.resolve_owner_dsn", lambda: target)
        upgrade(target, "ltm", "l13_message_split", None, tmp_path)
        dump = create_dump(admin, tmp_path / "old.dump", tmp_path, shared_peers=True)
        selection = tmp_path / "honcho-config.json"
        selection.write_text(json.dumps({"hosts": {"hermes": {"workspace": "alpha", "aiPeer": "source-ai",
                                                            "peerName": "user", "observationMode": "directional"}}}))
        report = migrate_honcho(dump, "default", "installed-ai", tmp_path, selection_path=selection,
                                restore_command=pg_command("pg_restore"))
        assert report["ok"] and report["source_revision"] == "3e7a1c9d5b20"
        assert report["inserted_text"] == 4
        with psycopg.connect(target) as conn:
            assert conn.execute("SELECT count(*) FROM ltm.message_meta WHERE workspace_name='installed-ai'").fetchone()[0] == 4
            assert conn.execute("SELECT DISTINCT observer FROM ltm.collections").fetchall() == [("installed-ai",)]

        # The actual entrypoint must also process two source profiles sharing this archive.
        from tests.hermes_cli.migrate_memory_fixtures import legacy_home
        from tests._fixtures.memory_store import APP_PASSWORD

        source = legacy_home(tmp_path / "source")
        home = tmp_path / "lina"
        (home / "store").mkdir(parents=True)
        (home / "store/store.env").write_text("")
        for profile, peer in (("alpha", "source-ai"), ("beta", "other-ai")):
            (source / "profiles" / profile / "honcho.json").write_text(json.dumps({"hosts": {
                f"hermes_{profile}": {"workspace": "alpha", "aiPeer": peer, "peerName": "user"}}}))
        entry = ("import json,sys; from pathlib import Path; from functools import partial; "
                 "from hermes_cli import store; import hermes_state_store as s; "
                 "c=json.loads(sys.stdin.readline()); s.resolve_owner_dsn=lambda:c['dsn']; "
                 "store.COMPOSE_FILE=Path(c['compose']); store.Store=partial(store.Store,project=c['project']); "
                 "store.HONCHO_IMAGE=c['image']; from hermes_cli.main import main; main()")
        context = json.dumps({"dsn": target, "compose": str(compose), "project": project,
                              "image": store.HONCHO_IMAGE}) + "\n"
        command = [sys.executable, "-c", entry, "migrate", "memory", "--from", str(source),
                   "--all-source-profiles", "--as", "copied", "--work-dir", str(tmp_path / "copy"),
                   "--honcho-dump", str(dump)]
        env = {**os.environ, "LINA_HOME": str(home), "HERMES_HOME": str(home),
               "LINA_STORE_DSN": target.replace("user=lina password=fixture", f"user=lina_app password={APP_PASSWORD}")}
        for repeated in (False, True):
            done = subprocess.run(command, input=context, env=env, capture_output=True,
                                  text=True, check=False, timeout=180)
            assert done.returncode == 0, done.stdout + done.stderr
            result = json.loads(done.stdout)
            assert result["ok"] and len(result["profiles"]) == 2
            if repeated:
                assert all(row["honcho"]["inserted"] == 0 for row in result["profiles"])
    finally:
        subprocess.run(installed.compose_argv("down", "--volumes"), capture_output=True, check=True, timeout=90)
