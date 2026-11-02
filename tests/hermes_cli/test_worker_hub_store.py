"""The hub's job records under overlapping writers (lina#35, review of #128): each save has its own temp file, and a
read-modify-write of a job runs under that job's lock, so neither a save nor an update is lost."""

import fcntl
import json
import os
import threading

from hermes_cli.worker import hub_store
from hermes_cli.worker.hub_store import Store

TASK = "OT-SIO1102120030"


def _store(tmp_path) -> Store:
    store = Store(tmp_path / "worker")
    store.save({"id": TASK, "created": 1.0})
    return store


def test_overlapping_saves_of_one_job_both_land(tmp_path, monkeypatch):
    store, real_replace, nested = _store(tmp_path), os.replace, []

    def replace(src, dst):
        if not nested:
            nested.append(True)
            store.save({"id": TASK, "created": 1.0, "by": "second"})  # a second writer between write and rename
        real_replace(src, dst)

    monkeypatch.setattr(hub_store.os, "replace", replace)
    store.save({"id": TASK, "created": 1.0, "by": "first"})
    assert json.loads(store.path(TASK).read_text())["by"] == "first"
    assert [p.name for p in store.jobs.iterdir()] == [f"{TASK}.json"]


def test_an_update_waits_for_a_running_edit_and_keeps_its_change(tmp_path, monkeypatch):
    store, real_flock, waiting = _store(tmp_path), fcntl.flock, threading.Event()

    def flock(handle, op):
        if threading.current_thread().name == "second-writer" and op & fcntl.LOCK_EX:
            waiting.set()
        return real_flock(handle, op)

    monkeypatch.setattr(hub_store.fcntl, "flock", flock)
    second = threading.Thread(target=store.update, args=(TASK,), kwargs={"verdict": "accepted"}, name="second-writer")
    with store.edit(TASK) as job:
        job["cancel_why"] = "not needed"
        second.start()
        assert waiting.wait(10)
    second.join(10)
    assert not second.is_alive()
    stored = store.load(TASK)
    assert stored["cancel_why"] == "not needed" and stored["verdict"] == "accepted"
