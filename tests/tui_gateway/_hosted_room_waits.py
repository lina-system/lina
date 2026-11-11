"""Waits for the hosted-room runtime tests that never bet on wall-clock time (lina#529).

On a loaded runner a 0.4 s real-time driver lease expired between renewals and fenced the
turn ("driver lease is stale or expired"), and a 1 s ``Event.wait`` missed a submit that did
happen. Runtimes here hold a lease far longer than any test; a test about lease expiry injects
``clock=`` and moves it. Waits are generous deadlines that name the last state they saw.
"""

from __future__ import annotations

import threading
import time
from pathlib import Path

from gateway import hosted_room_driver as state
from tui_gateway.hosted_room_driver import HostedRoomRuntime

_LEASE_TTL_SECONDS = 30.0


def _wait_for(predicate, *, timeout: float = 10.0, describe=None) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.01)
    last = f"; last state: {describe()}" if describe is not None else ""
    raise AssertionError(f"condition was not reached before timeout{last}")


def _task_state(db: Path, identity: state.TaskIdentity, runtime: HostedRoomRuntime):
    """Describe a wait's last state, so a timeout names what the runtime saw."""
    return lambda: {
        "status": state.get_task(db, identity)["status"],
        "last_error": runtime.status()["last_error"],
    }


def _wait_status(
    db: Path, identity: state.TaskIdentity, runtime: HostedRoomRuntime, status: str,
) -> None:
    _wait_for(lambda: state.get_task(db, identity)["status"] == status,
              describe=_task_state(db, identity, runtime))


def _wait_set(event: threading.Event, runtime: HostedRoomRuntime) -> None:
    assert event.wait(10.0), f"event never set; runtime status: {runtime.status()}"
