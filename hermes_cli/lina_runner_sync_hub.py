"""What the daily runner sync says about the hub itself (lina#238).

* The hub's devices name: ``Hub._hub_node``'s rule, the first ``host: local`` device in ``devices.workers``; ``hub``
  when there is none. Sign-in questions name it, so ``lina devices sign-in <name> <runner>`` finds the device.
* ``hub prepare needed`` (no LINA copy of the runner on the hub) is an event, so it reaches the notice, and an issue
  line; it is not a hold, a later ``lina runners install`` lets the next day's run move it.
* ``hub busy``: the daily run's runners that the hub was too busy for are written into ``daily.json`` (``retry``,
  ``busy``), and the serve tick starts one more run for them (``lina_runner_sync_daily``). That run covers only those
  runners, so the day's record keeps what the first run found for the others.
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


def busy(record: dict) -> list[str]:
    """The run's runners the hub was too busy for."""
    return _in_state(record, BUSY)


def day_record(path: Path, record: dict) -> dict:
    """*record* laid over the day's earlier record: a run of some runners keeps the others' results."""
    earlier = state.read_json(path, {})
    if not isinstance(earlier, dict) or not earlier.get("runners"):
        return record
    questions = {ask["runner"]: ask for ask in [*(earlier.get("questions") or []), *record["questions"]]}
    return {**record, "runners": {**earlier["runners"], **record["runners"]},
            "events": [*(earlier.get("events") or []), *record["events"]],
            "alerts": [*(earlier.get("alerts") or []), *record.get("alerts", [])], "questions": list(questions.values())}
