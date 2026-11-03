"""``WorkerHubRecord`` turns each ``lina worker`` hub state into running / ended / free for ``lina work next``. A job
the hub has placed but not yet started (``placing``) still holds its contract (lina#63 delivery step 5)."""

from __future__ import annotations

import json

import pytest

from hermes_cli.delivery.record import WorkerHubRecord


@pytest.mark.parametrize("hub_state, kind", [
    ("new", "running"), ("queued", "running"), ("placing", "running"), ("running", "running"),
    ("done", "ended"), ("review", "ended"), ("stalled", "ended"),
    ("failed", "free"), ("cancelled", "free"),
])
def test_each_hub_state_maps_to_one_record_state(tmp_path, hub_state, kind):
    jobs = tmp_path / "worker" / "hub" / "jobs"
    jobs.mkdir(parents=True)
    (jobs / "OT-A1.json").write_text(json.dumps({"id": "OT-A1", "state": hub_state, "created": 1.0,
                                                 "repo": "acme/app", "topic": "acme/app#7"}), encoding="utf-8")
    (row,) = WorkerHubRecord(tmp_path).jobs()
    assert (row["id"], row["state"]) == ("OT-A1", kind)
