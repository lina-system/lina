"""What the daily runner sync says about the hub itself (lina#238).

* The hub's devices name: ``Hub._hub_node``'s rule, the first ``host: local`` device in ``devices.workers``; ``hub``
  when there is none. Sign-in questions name it, so ``lina devices sign-in <name> <runner>`` finds the device.
* ``hub prepare needed`` (no LINA copy of the runner on the hub) is an event, so it reaches the notice, and an issue
  line; it is not a hold, a later ``lina runners install`` lets the next day's run move it.
* ``hub busy``: the run's runners that the hub was too busy for are written into ``last.json`` (``retry``, ``busy``),
  and the serve tick starts one more run for them (``lina_runner_sync_daily``). That run covers only those runners,
  so the day's record keeps what the first run found for the others.
"""

from __future__ import annotations

from pathlib import Path

from hermes_cli import lina_runner_sync_state as state

PREPARE = "hub prepare needed"
BUSY = "hub busy"
FALLBACK_NAME = "hub"


def hub_name(root: Path) -> str:
    from hermes_cli.worker import settings
    return next((dev.name for dev in settings.devices(root) if dev.local), FALLBACK_NAME)


def prepare_event(name: str, newest: str) -> str:
    return f"{name} hub {newest}: not moved, {name} is not installed on the hub (`lina runners install {name}`)"


def _in_state(record: dict, wanted: str) -> list[str]:
    return [name for name, rec in sorted((record.get("runners") or {}).items())
            if (rec.get("hub") or {}).get("state") == wanted]


def prepare_issues(record: dict) -> list[str]:
    return [f"- prepare: {name} is not installed on the hub; run `lina runners install {name}`"
            for name in _in_state(record, PREPARE)]


def carried(last: dict, now: float) -> dict:
    """What a run's start keeps from ``last.json``: that today's retry was already started."""
    started = last.get("started")
    same_day = isinstance(started, (int, float)) and state.day(started) == state.day(now)
    return {"retried": True} if same_day and last.get("retried") else {}


def retry_mark(record: dict, last: dict) -> dict:
    """``retry`` and the busy runners after a first run of the day that found the hub busy; nothing after the retry."""
    busy = _in_state(record, BUSY)
    return {"retry": True, "busy": busy} if busy and not last.get("retried") else {}


def day_record(path: Path, record: dict) -> dict:
    """*record* laid over the day's earlier record: a run of some runners keeps the others' results."""
    earlier = state.read_json(path, {})
    if not isinstance(earlier, dict) or not earlier.get("runners"):
        return record
    questions = {ask["runner"]: ask for ask in [*(earlier.get("questions") or []), *record["questions"]]}
    return {**record, "runners": {**earlier["runners"], **record["runners"]},
            "events": [*(earlier.get("events") or []), *record["events"]], "questions": list(questions.values())}
