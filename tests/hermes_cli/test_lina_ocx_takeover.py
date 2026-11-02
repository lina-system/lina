"""``lina models takeover plan`` / ``copy`` and ``lina models refresh`` (lina#33) on a fake old OCX data dir: the
plan names what moves and never prints a secret, the copy goes through SQLite's backup API, verifies against its
manifest and leaves the source as it was, and the credit refresh cron entry starts disabled."""
import argparse
import hashlib
import json
import sqlite3
from pathlib import Path

import pytest

from hermes_cli import lina_ocx, lina_ocx_takeover as takeover, lina_ocx_takeover_refresh as refresh
from hermes_cli.subcommands.models import build_models_parser
from tests.hermes_cli._lina_ocx_fakes import installed_root

TOKEN = "FAKE-PLANTED-TOKEN-7d1c"


def old_data_dir(path: Path, *, hostname="127.0.0.2", port=18555, loopback_port=18556) -> Path:
    """A small OCX data dir shaped like a running one: config, secrets, a WAL database, runtime files, backups."""
    (path / "backups").mkdir(parents=True)
    (path / "logs").mkdir()
    (path / "config.json").write_text(json.dumps({
        "port": port, "hostname": hostname,
        "unauthenticatedLoopbackListener": {"enabled": True, "port": loopback_port},
        "providers": {"openai": {"apiKey": "keychain:openai"}, "plain": {"apiKey": TOKEN},
                      "pool": {"apiKeyPool": ["keychain:pool/p1", "keychain:pool/p2"]}}}))
    for name in ("service-api-token", "admin-api-token"):
        (path / name).write_text(TOKEN + "\n")
    (path / "auth.json").write_text(json.dumps({"access_token": TOKEN}))
    (path / "ocx.pid").write_text("4242\n")
    (path / "runtime-port.json").write_text(json.dumps({"port": port}))
    (path / "serving-runtimes.json").write_text("{}")
    (path / "crash.log").write_text("boom\n")
    (path / "state.lock").write_text("")
    (path / "backups" / "config.json.1").write_text("{}")
    (path / "logs" / "proxy.log").write_text("line\n")
    (path / "usage.jsonl").write_text('{"tokens": 1}\n')
    db = sqlite3.connect(path / "routing-history.sqlite")
    db.execute("PRAGMA journal_mode=WAL")
    db.execute("CREATE TABLE history (id INTEGER PRIMARY KEY, route TEXT)")
    db.executemany("INSERT INTO history (route) VALUES (?)", [(f"r{i}",) for i in range(200)])
    db.commit()
    db.close()
    return path


def _state(path: Path) -> dict[str, tuple[str, int]]:
    """sha256 and mtime_ns of every file except SQLite's shared-memory index, which any reader maps and writes."""
    return {str(p.relative_to(path)): (hashlib.sha256(p.read_bytes()).hexdigest(), p.stat().st_mtime_ns)
            for p in sorted(path.rglob("*")) if p.is_file() and not p.name.endswith("-shm")}


def _models(*argv) -> int:
    parser = argparse.ArgumentParser()
    build_models_parser(parser.add_subparsers())
    args = parser.parse_args(["models", *argv])
    with pytest.raises(SystemExit) as exc:
        args.func(args)
    return exc.value.code


def isolate_old_job_dirs(tmp_path, monkeypatch, kind: str = "systemd") -> None:
    """The old job is looked up under tmp_path for both kinds, never in the developer's real LaunchAgents or
    systemd user unit dir (a machine running OCX has one there)."""
    from hermes_cli import gateway, lina_ocx_service
    monkeypatch.setattr(lina_ocx_service, "backend", lambda: kind)
    monkeypatch.setattr(takeover, "launch_agents_dir", lambda: tmp_path / "LaunchAgents")
    monkeypatch.setattr(gateway, "user_systemd_unit_dir", lambda: tmp_path / "units")


@pytest.fixture
def root(tmp_path, monkeypatch):
    root = installed_root(tmp_path / "lina")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    isolate_old_job_dirs(tmp_path, monkeypatch)
    return root


def test_plan_lists_what_is_copied_and_skipped_and_never_prints_a_secret(root, tmp_path, capsys):
    source = old_data_dir(tmp_path / "old")
    assert _models("takeover", "plan", "--from", str(source), "--argv", "bun index.ts start --port 18557") == 0
    assert _models("takeover", "plan", "--from", str(source), "--json") == 0
    captured = capsys.readouterr()
    assert TOKEN not in captured.out and TOKEN not in captured.err

    data = takeover.build_plan(source)
    actions = {row["name"]: row["action"] for row in data["entries"]}
    for name in ("ocx.pid", "runtime-port.json", "serving-runtimes.json", "crash.log", "state.lock", "backups"):
        assert actions[name] == "skip", name
    for name in ("config.json", "usage.jsonl", "service-api-token", "admin-api-token", "auth.json", "logs/"):
        assert actions[name] == "copy", name
    kinds = {row["name"]: row.get("kind") for row in data["entries"]}
    assert kinds["routing-history.sqlite"] == "sqlite" and kinds["service-api-token"] == "secret"
    assert data["keychain"]["refs"] == ["keychain:openai", "keychain:pool/p1", "keychain:pool/p2"]
    assert [(item["role"], item["host"], item["port"], item["token_required"]) for item in data["listeners"]] == [
        ("main", "127.0.0.2", 18555, True), ("loopback", "127.0.0.1", 18556, False)]
    assert takeover.build_plan(source, argv="bun index.ts start --port 18557")["listeners"][0]["port"] == 18557
    assert data["old_job"]["name"] == "opencodex-proxy" and data["old_job"]["found"] is False


@pytest.mark.parametrize(("kind", "name", "definition"), [
    ("systemd", "opencodex-proxy", "units/opencodex-proxy.service"),
    ("launchd", "com.opencodex.proxy", "LaunchAgents/com.opencodex.proxy.plist"),
])
def test_plan_finds_the_old_job_of_either_kind_only_where_the_test_put_it(root, tmp_path, monkeypatch, kind, name,
                                                                         definition):
    isolate_old_job_dirs(tmp_path, monkeypatch, kind)
    source = old_data_dir(tmp_path / "old")
    job = takeover.build_plan(source)["old_job"]
    assert (job["kind"], job["name"], job["path"], job["found"]) == (kind, name, str(tmp_path / definition), False)
    (tmp_path / definition).parent.mkdir(parents=True, exist_ok=True)
    (tmp_path / definition).write_text("")
    assert takeover.build_plan(source)["old_job"]["found"] is True


@pytest.mark.platforms("posix")
def test_copy_uses_the_backup_api_with_a_writer_open_verifies_and_leaves_the_source_alone(root, tmp_path):
    source = old_data_dir(tmp_path / "old")
    writer = sqlite3.connect(source / "routing-history.sqlite")
    writer.execute("PRAGMA wal_autocheckpoint=0")
    writer.executemany("INSERT INTO history (route) VALUES (?)", [(f"w{i}",) for i in range(50)])
    writer.commit()  # committed rows live only in the WAL file
    writer.execute("BEGIN IMMEDIATE")
    writer.execute("INSERT INTO history (route) VALUES ('uncommitted')")
    assert (source / "routing-history.sqlite-wal").stat().st_size > 0
    before = _state(source)
    try:
        assert takeover.copy(source, root) == 0
        assert _state(source) == before  # before the writer's close checkpoints its WAL into the database
    finally:
        writer.rollback()
        writer.close()

    data = lina_ocx.data_dir(root)
    copied = sqlite3.connect(data / "routing-history.sqlite")
    assert copied.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    assert copied.execute("SELECT count(*) FROM history").fetchone()[0] == 250
    copied.close()
    assert not (data / "ocx.pid").exists() and not (data / "backups").exists() and not (data / "crash.log").exists()
    assert (data / "service-api-token").read_text() == TOKEN + "\n"
    manifest = json.loads(takeover.manifest_path(root).read_text())
    assert {item["path"] for item in manifest["files"]} >= {"config.json", "routing-history.sqlite",
                                                           "logs/proxy.log", "service-api-token"}
    assert takeover.verify(data, manifest) == []
    (data / "usage.jsonl").write_text("changed\n")
    assert takeover.verify(data, manifest) == ["usage.jsonl"]


def test_copy_refuses_existing_data_and_replace_moves_it_aside(root, tmp_path, capsys):
    source = old_data_dir(tmp_path / "old")
    assert takeover.copy(source, root) == 0
    (lina_ocx.data_dir(root) / "lina-only.txt").write_text("keep me")
    assert takeover.copy(source, root) == 1
    assert "--replace" in capsys.readouterr().out
    assert takeover.copy(source, root, replace=True) == 0
    (aside,) = root.glob("ocx.replaced-*")
    assert (aside / "lina-only.txt").read_text() == "keep me"
    assert not (lina_ocx.data_dir(root) / "lina-only.txt").exists()


def test_copy_creates_the_credit_refresh_cron_entry_disabled_and_enable_turns_it_on(root, tmp_path):
    from cron import jobs
    source = old_data_dir(tmp_path / "old")
    assert _models("takeover", "copy", "--from", str(source)) == 0
    with jobs.use_cron_store(root):
        (job,) = [j for j in jobs.list_jobs(include_disabled=True) if j["name"] == refresh.JOB_NAME]
    assert job["enabled"] is False and job["no_agent"] is True and job["schedule"]["minutes"] == 2
    assert (root / "scripts" / refresh.SCRIPT_NAME).is_file()
    refresh.enable_job(root, out=lambda line: None)
    with jobs.use_cron_store(root):
        (job,) = [j for j in jobs.list_jobs(include_disabled=True) if j["name"] == refresh.JOB_NAME]
        assert job["enabled"] is True
        assert len(jobs.list_jobs(include_disabled=True)) == 1


def test_refresh_runs_ocx_account_refresh_for_each_pooled_provider(root, monkeypatch, capsys):
    import subprocess
    from hermes_cli import lina_models
    listing = {"accounts": [{"provider": "openai", "type": "codex"}, {"provider": "openai", "type": "codex"},
                            {"provider": "anthropic", "type": "oauth"}, {"provider": "keyed", "type": "api-key"}]}
    monkeypatch.setattr(lina_models, "query", lambda view, root=None: listing)
    calls = []

    def run(argv, **kwargs):
        calls.append(argv[2:])
        failed = argv[4] == "anthropic"
        return subprocess.CompletedProcess(argv, 1 if failed else 0, "", "quota probe failed" if failed else "")

    monkeypatch.setattr(refresh.subprocess, "run", run)
    assert _models("refresh") == 1
    assert calls == [["account", "refresh", "openai", "--json"], ["account", "refresh", "anthropic", "--json"]]
    assert "✗ anthropic: quota probe failed" in capsys.readouterr().out
    calls.clear()
    assert _models("refresh", "openai") == 0
    assert calls == [["account", "refresh", "openai", "--json"]]
