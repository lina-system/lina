"""Schedule, batch and reserved-ID ownership through the caller shim (lina#430)."""

import json
from pathlib import Path

import pytest

from hermes_cli import lina_shims
from hermes_cli.worker.hub_store import Store


@pytest.fixture
def store(tmp_path: Path) -> Store:
    (tmp_path / "config.yaml").write_text("worker:\n  legacy_omo_task: /old/omo-task\n", encoding="utf-8")
    return Store(tmp_path / "worker")


@pytest.mark.parametrize("prefix,verb", [
    ("OS", "unschedule"), ("OG", "status"), ("OG", "cancel"), ("OG", "receipts"), ("OT", "status"),
])
@pytest.mark.parametrize("collision", [False, True], ids=["first", "clash"])
def test_minted_ids_route_to_lina_without_job_records(store, monkeypatch, prefix, verb, collision):
    monkeypatch.setattr("hermes_cli.worker.hub_store.time.strftime", lambda fmt: "1109000000")
    if collision:
        store.new_id("qa", prefix)
    task_id = store.new_id("qa", prefix)
    assert task_id == f"{prefix}-QA1109000000" + ("-2" if collision else "")

    routed = lina_shims.route("omo-task", [verb, task_id], store.worker_root.parent)

    assert (routed.to, routed.argv) == ("lina", ["worker", verb, task_id])


@pytest.mark.parametrize("kind,verb", [
    ("job", "status"), ("schedule", "unschedule"),
    ("batch", "status"), ("batch", "cancel"), ("batch", "receipts"),
])
def test_held_records_route_to_lina_without_id_reservations(store, kind, verb):
    task_id = {"job": "OT-QA1", "schedule": "OS-QA1", "batch": "OG-QA1"}[kind]
    match kind:
        case "job":
            store.save({"id": task_id})
        case "schedule":
            store.schedules.mkdir(parents=True)
            (store.schedules / f"{task_id}.json").write_text(json.dumps({"id": task_id}), encoding="utf-8")
        case "batch":
            store.receipt("OT-MEMBER1", "submitted", group=task_id)

    routed = lina_shims.route("omo-task", [verb, task_id], store.worker_root.parent)

    assert (routed.to, routed.argv) == ("lina", ["worker", verb, task_id])


@pytest.mark.parametrize("task_id,verb", [
    ("OS-OLD1", "unschedule"), ("OG-OLD1", "status"), ("OG-OLD1", "cancel"),
    ("OG-OLD1", "receipts"), ("OT-OLD1", "status"), ("OG-OLD1-2", "status"),
])
def test_unknown_ids_stay_with_legacy_and_are_not_reserved(store, task_id, verb):
    store.new_id("other", "OG")
    store.receipt("OT-OTHER1", "submitted", group="OG-OTHER1")

    routed = lina_shims.route("omo-task", [verb, task_id], store.worker_root.parent)

    assert (routed.to, routed.argv) == ("legacy", ["/old/omo-task", verb, task_id])
    assert not (store.root / "ids" / task_id).exists()
