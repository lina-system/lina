"""Batch IDs resolve through member jobs for status and cancel (lina#431)."""

import argparse
import json

import pytest

from hermes_cli.subcommands.worker import build_worker_parser
from hermes_cli.worker.hub import Hub
from tests.hermes_cli import _worker_fakes as fakes


@pytest.fixture
def batch(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    fakes.write_config(base, "{}\n")
    monkeypatch.setenv("LINA_HOME", str(base))
    hub = Hub(base / "worker")
    for task_id, state, verdict in (("OT-QA1", "running", None), ("OT-QA2", "done", "accepted")):
        hub.store.save({"id": task_id, "group": "OG-QA", "state": state, "host": "here", "created": 1,
                        "bot": "tester", "topic": "batch", "mode": "ulw", "runner": "command",
                        "runs": 1, "verdict": verdict, "delivery": "pending", "brief": "brief.md",
                        "prompt": "private brief", "notified": []})
        task_dir = hub.worker_root / "tasks" / task_id
        task_dir.mkdir(parents=True)
        (task_dir / "state").write_text(state, encoding="utf-8")
        (task_dir / "prompt.1").write_text("brief", encoding="utf-8")
        if state == "done":
            (task_dir / "rc.1").write_text("0", encoding="utf-8")
    return hub


def command(*argv):
    parser = argparse.ArgumentParser()
    build_worker_parser(parser.add_subparsers())
    args = parser.parse_args(["worker", *argv])
    with pytest.raises(SystemExit) as exit_info:
        args.func(args)
    return exit_info.value.code


@pytest.mark.parametrize("sent", [False, True])
@pytest.mark.parametrize("as_json", [False, True])
def test_batch_status_text_and_json(batch, capsys, sent, as_json):
    if sent:
        batch.store.locks.mkdir(parents=True, exist_ok=True)
        (batch.store.locks / "OG-QA.sent").write_text("sent", encoding="utf-8")
    assert command("status", "OG-QA", *(["--json"] if as_json else [])) == 0
    output = capsys.readouterr().out
    if not as_json:
        lines = output.splitlines()
        assert len(lines) == 3
        assert "OT-QA1" in lines[0] and "running" in lines[0] and "here" in lines[0]
        assert "no verdict yet" in lines[0]
        assert "OT-QA2" in lines[1] and "done" in lines[1] and "accepted" in lines[1]
        assert "OG-QA" in lines[2] and "1/2" in lines[2]
        assert ("notice sent" if sent else "notice not sent") in lines[2]
        return
    data = json.loads(output)
    assert (data["id"], data["done"], data["total"], data["notified"]) == ("OG-QA", 1, 2, sent)
    assert [(job["id"], job["state"], job["host"], job["verdict"]) for job in data["members"]] == [
        ("OT-QA1", "running", "here", None), ("OT-QA2", "done", "here", "accepted")]
    assert all("prompt" not in job for job in data["members"])


@pytest.mark.parametrize("state", ["running", "queued", "unknown"])
def test_batch_cancel_preserves_ended_members(batch, capsys, state):
    batch.store.update("OT-QA1", state=state, host=None if state == "queued" else "here")
    ended = batch.store.path("OT-QA2").read_bytes()
    assert command("cancel", "OG-QA", "--why", "stop-batch") == 0
    assert "1 cancelled" in capsys.readouterr().out
    job = batch.need("OT-QA1")
    assert job["state"] == "cancelled" and job["cancel_why"] == "stop-batch"
    assert batch.store.path("OT-QA2").read_bytes() == ended
    assert not (batch.worker_root / "tasks" / "OT-QA2" / "cancelled").exists()
    assert [r["why"] for r in batch.store.receipts_of("OT-QA1") if r["kind"] == "cancel_requested"] == ["stop-batch"]
    assert not batch.store.receipts_of("OT-QA2")
    assert len([r for r in batch.store.receipts_of("OG-QA") if r["kind"] == "group_notified"]) == 1
    assert command("cancel", "OG-QA", "--why", "again") == 0
    assert "0 cancelled" in capsys.readouterr().out
    assert batch.need("OT-QA1")["cancel_why"] == "stop-batch"


@pytest.mark.parametrize("argv", [("status", "OG-MISSING"), ("status", "OG-MISSING", "--json"),
                                  ("cancel", "OG-MISSING", "--why", "stop-batch")])
def test_unknown_batch_still_fails(batch, capsys, argv):
    assert command(*argv) == 2
    assert "OG-MISSING: no such task" in capsys.readouterr().err
