"""Root-owned external message CLI and client API contracts (lina#23)."""

from __future__ import annotations

import argparse
import os
import subprocess
import sys

import pytest

from hermes_cli.config import read_user_config_raw
from hermes_cli.external_messages import enabled, ingest
from hermes_cli.main_agent_cmds import cmd_memory
from hermes_cli.subcommands.memory import build_memory_parser
from tests.hermes_cli.test_external_messages import event, message
from utils import atomic_roundtrip_yaml_update


def _parse(*words):
    parser = argparse.ArgumentParser()
    build_memory_parser(parser.add_subparsers(), cmd_memory=cmd_memory)
    return parser.parse_args(["memory", "external-messages", *words])


def _cli(store, home, *words):
    # Only the owner-DSN resolver is replaced: production always names database lina,
    # whereas the fixture gives this test process its own isolated database.
    script = ("import sys; import hermes_state_store as s; dsn=sys.argv.pop(1); "
              "s.resolve_owner_dsn=lambda:dsn; "
              "from hermes_cli.main import main; main()")
    return subprocess.run(
        [sys.executable, "-c", script, store.superuser_dsn(),
         "memory", "external-messages", *words],
        env={**os.environ, "HERMES_HOME": str(home), "LINA_HOME": str(home)},
        capture_output=True, text=True, encoding="utf-8", errors="replace",
        timeout=60, check=False,
    )


@pytest.mark.parametrize("action", ["on", "off"])
def test_parser_handler_writes_root_not_bound_profile(tmp_path, monkeypatch, action):
    # Given a root config with unrelated settings and a different bound profile.
    import hermes_constants
    root = tmp_path / "root"
    path = root / "config.yaml"
    atomic_roundtrip_yaml_update(path, "memory.external_messages.spool", "/private/spool")
    atomic_roundtrip_yaml_update(path, "memory.provider", "honcho")
    atomic_roundtrip_yaml_update(path, "display.skin", "ares")
    profile = root / "profiles" / "writer"
    profile.mkdir(parents=True)
    atomic_roundtrip_yaml_update(profile / "config.yaml", "memory.external_messages.enabled", True)
    before = (profile / "config.yaml").read_bytes()
    monkeypatch.setattr(hermes_constants, "get_default_hermes_root", lambda: root)

    # When the real parser dispatches a switch action under that profile.
    token = hermes_constants.set_hermes_home_override(profile)
    try:
        args = _parse(action)
        args.func(args)
    finally:
        hermes_constants.reset_hermes_home_override(token)

    # Then the root changes without losing other keys or touching the profile.
    config = read_user_config_raw(path)
    assert config["memory"]["external_messages"] == {"spool": "/private/spool", "enabled": action == "on"}
    assert config["memory"]["provider"] == "honcho"
    assert config["display"]["skin"] == "ares"
    assert (profile / "config.yaml").read_bytes() == before
    assert enabled() is (action == "on")


@pytest.mark.parametrize("action", ["on", "off"])
def test_subprocess_switch_persists_root_without_changing_rows(
    tmp_path, monkeypatch, _memory_store, memory_store_sql, action,
):
    # Given collected data and an opposite switch value.
    home = tmp_path / "home"
    monkeypatch.setenv("HERMES_HOME", str(home))
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", True)
    spool = tmp_path / "spool.jsonl"
    spool.write_bytes(event(message()))
    ingest(spool)
    rows = memory_store_sql("SELECT uid, content, observed_at FROM memory.messages")
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", action != "on")

    # When a separate real CLI process changes the switch.
    done = _cli(_memory_store, home, action)

    # Then the switch persists and stored rows remain untouched.
    assert done.returncode == 0, done.stdout + done.stderr
    assert read_user_config_raw(home / "config.yaml")["memory"]["external_messages"]["enabled"] is (action == "on")
    assert memory_store_sql("SELECT uid, content, observed_at FROM memory.messages") == rows


def test_status_subprocess_reports_aggregates_without_identifiers(
    tmp_path, monkeypatch, _memory_store, memory_store_sql,
):
    # Given one external row, an unrelated row, and collection currently off.
    home = tmp_path / "home"
    monkeypatch.setenv("HERMES_HOME", str(home))
    spool = tmp_path / "private-spool.jsonl"
    spool.write_bytes(event(message(room="private-room", text="private-content")))
    ingest(spool)
    memory_store_sql("UPDATE memory.messages SET observed_at='2026-02-03T04:05:06Z'")
    memory_store_sql("INSERT INTO memory.messages(uid,source,agent_id,scope,content,content_sha256) "
                     "VALUES ('private-agent-row','agent','a','agent','private','digest')")
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", False)

    # When status runs against the real store in a separate process.
    done = _cli(_memory_store, home, "status")

    # Then only the switch and external collection aggregates are exposed.
    assert done.returncode == 0, done.stdout + done.stderr
    assert done.stdout.strip().splitlines() == [
        "External messages: off", "Stored messages: 1", "Last collection: 2026-02-03T04:05:06+00:00",
    ]
    for private in (str(home), str(spool), "private-room", "private-content", "discord", "writer", "localhost"):
        assert private not in done.stdout + done.stderr


def test_status_parser_reports_empty_collection(memory_store_sql, capsys):
    # Given an empty real store and the default-on switch.
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]
    # When the real parser and memory dispatch handle status.
    args = _parse("status")
    args.func(args)
    # Then absence is represented without inventing a collection timestamp.
    assert capsys.readouterr().out.strip().splitlines() == [
        "External messages: on", "Stored messages: 0", "Last collection: never",
    ]


@pytest.mark.parametrize("value", [False, True])
def test_root_config_api_switch_matches_cli_status(
    tmp_path, monkeypatch, _memory_store, memory_store_sql, value,
):
    # Given a root-owned switch and the production FastAPI route with real auth.
    from starlette.testclient import TestClient
    from hermes_cli.web_server import _SESSION_HEADER_NAME, _SESSION_TOKEN, app

    home = tmp_path / "home"
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(home))
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.enabled", not value)
    atomic_roundtrip_yaml_update(home / "config.yaml", "memory.external_messages.spool", "/private/spool")
    atomic_roundtrip_yaml_update(home / "config.yaml", "display.skin", "ares")
    assert memory_store_sql("SELECT count(*) FROM memory.messages") == [(0,)]

    # When a client explicitly targets the default/root profile.
    with TestClient(app) as client:
        response = client.put(
            "/api/config?profile=default", headers={_SESSION_HEADER_NAME: _SESSION_TOKEN},
            json={"config": {"memory": {"external_messages": {"enabled": value}}}},
        )

    # Then persisted config and a separate CLI process observe the same value.
    assert response.status_code == 200, response.text
    config = read_user_config_raw(home / "config.yaml")
    assert config["memory"]["external_messages"] == {"enabled": value, "spool": "/private/spool"}
    assert config["display"]["skin"] == "ares"
    done = _cli(_memory_store, home, "status")
    assert done.returncode == 0, done.stdout + done.stderr
    assert done.stdout.splitlines()[0] == f"External messages: {'on' if value else 'off'}"
