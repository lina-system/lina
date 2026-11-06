"""The models view's last good list reaches a submit in another process (lina#283): every good read writes
``work-settings/models-last-good.json`` (only what the check uses), and a fresh ``lina worker submit`` process
decides the fallback from it while it is at most ten minutes old. OCX is a fake with synthetic rows; no device is
configured, so the job is only recorded (queued), never run."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path

import pytest

from hermes_cli import lina_models
from hermes_cli import lina_work_settings_models as wm
from hermes_cli.worker.hub_store import Store
from tests.hermes_cli.test_worker_settings_at_submit import CONFIG, LIVE, QUOTA

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
NAMED_QUOTA = {"accounts": [
    {**row, "name": "acct-name", "email": "someone@example.test", "label": "acct-label"} for row in QUOTA["accounts"]]}


@pytest.fixture
def root(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text("devices:\n  workers: {}\n" + CONFIG, encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(base))
    monkeypatch.setenv("HERMES_HOME", str(base))
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)
    answers = {("models", "live", "--json"): LIVE, ("account", "list", "--quota", "--json"): NAMED_QUOTA}
    monkeypatch.setattr(lina_models, "run", lambda args, root=None: answers[tuple(args)])
    wm.forget()
    yield base
    wm.forget()


def _submit(root: Path, tmp_path: Path) -> dict:
    """``lina worker submit`` as a new process: it has no in-memory read of its own."""
    brief = tmp_path / "brief.md"
    brief.write_text("do it", encoding="utf-8")
    done = subprocess.run([sys.executable, str(REPO / "lina"), "worker", "submit", "--brief", str(brief),
                           "--topic", "t", "--runner", "codex", "--bot", "sion"],
                          cwd=REPO, env=dict(os.environ), capture_output=True, text=True, timeout=120, check=False)
    assert done.returncode == 0, done.stdout + done.stderr
    jobs = Store(root / "worker").all()
    assert len(jobs) == 1, jobs
    return jobs[0]


def test_a_submit_in_a_new_process_runs_on_the_fallback_after_a_models_read_in_another(root, tmp_path):
    wm.read(time.time())

    job = _submit(root, tmp_path)

    assert (job["settings"]["model_source"], job["settings"]["ocx_check"]) == ("fallback:off", "off")
    assert (job["model"]["provider"], job["model"]["id"]) == ("prov", "m-open")
    kinds = [r["kind"] for r in Store(root / "worker").receipts_of(job["id"])]
    assert kinds.count("model_fallback") == 1


def test_a_snapshot_older_than_ten_minutes_leaves_the_check_unchecked(root, tmp_path):
    wm.read(time.time() - 11 * 60)
    assert json.loads((root / "work-settings" / "models-last-good.json").read_text(encoding="utf-8"))["rows"]

    job = _submit(root, tmp_path)

    assert (job["settings"]["model_source"], job["settings"]["ocx_check"]) == ("runner:codex", "unchecked")
    assert (job["model"]["provider"], job["model"]["id"], job["model"]["thinking"]) == ("prov", "m-off", "high")
    assert "model_fallback" not in [r["kind"] for r in Store(root / "worker").receipts_of(job["id"])]


def test_the_snapshot_keeps_no_account_name_email_id_or_label(root):
    wm.read(1_000.0)

    data = json.loads((root / "work-settings" / "models-last-good.json").read_text(encoding="utf-8"))

    assert data["at"] == 1_000.0
    assert all(not {"name", "email", "id", "label"} & set(a) for a in data["accounts"])
    assert [a["provider"] for a in data["accounts"]] == ["prov", "zed", "gone"]
    assert data["accounts"][0]["quota"] == {"weeklyPercent": 40}
    assert [(r["provider"], r["id"]) for r in data["rows"]] == [(r["provider"], r["id"]) for r in LIVE]


@pytest.mark.parametrize("text", ["", "not json", "[]", '{"at": "now", "rows": [], "accounts": []}',
                                  '{"at": 1000, "rows": {}, "accounts": []}'])
def test_a_missing_unreadable_or_wrong_typed_snapshot_is_no_list(root, text):
    path = root / "work-settings" / "models-last-good.json"
    wm.read(1_000.0)
    wm.forget()
    assert wm.last_good(1_000.0) is not None  # another process's good read

    path.unlink()
    if text:
        path.write_text(text, encoding="utf-8")

    assert wm.last_good(1_000.0) is None
