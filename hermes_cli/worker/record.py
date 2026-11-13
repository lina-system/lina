"""One ``lina worker`` run's own record, round by round (lina#201): what the run said and did in each round, newest
round first, for the web app's work-card timeline.

A round is one run of the task: run 1 opens at its ``run_started`` receipt (the job's ``started`` when there is
none), run n at the ``follow_up`` receipt that requested it. Its end is its own ``exec_end`` receipt (by run number;
an end with no run number is the only round's, else the round going at its time), never the next round's start.
``rounds`` decides these boundaries once; the run card's ``endedAt`` (``worker.board.cards``) and this record both
read it.

The run's session record lives on the build device that ran it; the hub reaches that device only through its
``_device`` verbs, and none of them returns the session. So the record serves what the hub keeps: its receipts (ends,
the owner's verdict notes and cancel reasons as comments), the fetched ``final.md`` and ``report.md`` (the last run's
only: each fetch replaces them), and for a round still going the runner's live view (its recent sentences and tool
steps, or its last line from a device that does not send them). The sentences of an earlier round that ended are not
kept on the hub.
"""

from __future__ import annotations

import json
from pathlib import Path

from hermes_cli.worker import kanban_link
from hermes_cli.worker.hub import ENDED, Hub
from hermes_cli.worker.hub_store import Store
from hermes_cli.worker.task import safe_id

STOPPED_ENDS = ("stalled", "failed", "cancelled", "done_empty", "unknown")
ROW_CHARS = 300
_LOW, _HIGH = float("-inf"), float("inf")


class RunNotFound(LookupError):
    pass


def receipts_by_task(store: Store) -> dict[str, list[dict]]:
    """The hub's receipts per run in the order they were written, from one pass over ``receipts.jsonl``."""
    found: dict[str, list[dict]] = {}
    if not store.receipts.is_file():
        return found
    for line in store.receipts.read_text(encoding="utf-8-sig").splitlines():
        try:
            row = json.loads(line)
        except ValueError:
            continue  # a line the hub is still appending
        if isinstance(row, dict) and isinstance(row.get("ts"), (int, float)) and isinstance(row.get("task"), str):
            found.setdefault(row["task"], []).append(row)
    return found


def _run_number(value) -> int | None:
    text = str(value if value is not None else "")
    return int(text) if text.isdigit() and int(text) >= 1 else None


def _span_at(rounds: list[dict], at: float) -> dict | None:
    """The round whose span (its start up to the next round's start) holds *at*."""
    for one in rounds:
        low = _LOW if one["start"] is None else one["start"]
        high = _HIGH if one["until"] is None else one["until"]
        if low <= at < high:
            return one
    return None


def round_at(rounds: list[dict], at: float) -> dict | None:
    """The round going at *at*; None after a round's own end and before the next round's start."""
    one = _span_at(rounds, at)
    return one if one is not None and (one["end"] is None or at <= one["end"]) else None


def rounds(job: dict, receipts: list[dict]) -> list[dict]:
    """The run's rounds oldest first: ``{run, start, until, end, end_state}``. When any round started, the first entry
    is the round whose start fell out of the kept history (``start`` None, up to the first known start); a reader
    keeps it only when something of its own falls in it. ``end`` is the round's own latest end receipt, else None."""
    starts: dict[int, float] = {}
    for row in receipts:
        kind = row.get("kind")
        if kind == "run_started" or (kind == "follow_up" and not row.get("queued")):
            run = _run_number(row.get("run")) or (1 if kind == "run_started" else None)
            if run is not None and run not in starts:
                starts[run] = float(row["ts"])
    if 1 not in starts and isinstance(job.get("started"), (int, float)):  # run 1's own start receipt fell out
        starts[1] = float(job["started"])
    ordered = sorted(starts.items(), key=lambda pair: pair[1])
    # The round whose start fell out of the kept history comes first. With no start on record at all it is the only
    # round and spans all time; the reader serves it only when something of its own (an end, a result) falls in it.
    out = [{"run": None, "start": None, "until": ordered[0][1] if ordered else None, "end": None, "end_state": None}]
    for index, (run, start) in enumerate(ordered):
        until = ordered[index + 1][1] if index + 1 < len(ordered) else None
        out.append({"run": run, "start": start, "until": until, "end": None, "end_state": None})
    by_run, first_run = {one["run"]: one for one in out[1:]}, (ordered[0][0] if ordered else None)
    for row in receipts:
        if row.get("kind") != "exec_end":
            continue
        run = _run_number(row.get("run"))
        if run is None:  # an older receipt or the hub's "unreachable" end: a run with one round has only that one
            own = out[1] if len(ordered) == 1 else _span_at(out, float(row["ts"]))
        else:
            own = by_run.get(run) or (out[0] if first_run is None or run < first_run else None)
        if own is not None and (own["end"] is None or row["ts"] >= own["end"]):
            own["end"], own["end_state"] = float(row["ts"]), row.get("state")
    return out


def ended_at(job: dict, receipts: list[dict]) -> float | None:
    """When the run ended as its card says it: the newest round's own end; a run that never started (cancelled while
    queued) ends at its task-level end receipt."""
    if job.get("state") not in ENDED:
        return None
    kept = rounds(job, receipts)
    if kept:
        return kept[-1]["end"]
    ends = [float(row["ts"]) for row in receipts if row.get("kind") == "exec_end"]
    return max(ends) if ends else None


def _line(text) -> str:
    return " ".join(str(text or "").split())[:ROW_CHARS]


def _first_line(path: Path) -> str:
    if not path.is_file():
        return ""
    for line in path.read_text(encoding="utf-8-sig", errors="replace").splitlines():
        text = line.strip().lstrip("-*> ").strip()
        if text and not text.startswith("#"):
            return _line(text)
    return ""


def _live(worker_root: Path, job: dict, now: float) -> tuple[list[tuple[float, str, str | None]], bool]:
    remote = Hub(worker_root, out=lambda *_: None).meta(job, live=True)
    if remote.get("unreachable"):
        return [], False
    live = remote.get("live") or {}
    if isinstance(live.get("tail"), list):
        entries = []
        for step in live["tail"]:
            if not isinstance(step, dict) or not isinstance(step.get("at"), (int, float)):
                continue
            if step.get("kind") == "tool":
                entries.append((float(step["at"]), "tool", None))
            elif step.get("kind") == "said" and _line(step.get("text")):
                entries.append((float(step["at"]), "said", _line(step["text"])))
        return entries, True
    last = _line(live.get("last"))
    at = live.get("last_at") if isinstance(live.get("last_at"), (int, float)) else now
    return ([(float(at), "said", last)] if last else []), True


def _fold(entries: list[tuple[float, str, str | None]]) -> list[dict]:
    """Rows oldest first: tool steps in a row become one ``tools`` row with their count and no text; one sentence
    said again in a row is one ``said`` row with its count."""
    rows: list[dict] = []
    for at, kind, text in entries:
        last = rows[-1] if rows else None
        if kind == "tool":
            if last is not None and last["kind"] == "tools":
                last["count"] += 1
            else:
                rows.append({"at": at, "kind": "tools", "text": None, "count": 1})
        elif kind == "said" and last is not None and last["kind"] == "said" and last["text"] == text:
            last["count"] += 1
        else:
            rows.append({"at": at, "kind": kind, "text": text, "count": 1})
    return rows


def _entries(store: Store, job: dict, receipts: list[dict]) -> tuple[list[tuple[float, str, str | None]], tuple]:
    """What the hub keeps of the run as ``(at, said|tool|comment, text)``, and the report's ``(time, first line)``."""
    entries: list[tuple[float, str, str | None]] = []
    for row in receipts:
        words = row.get("note") if row.get("kind") == "verdict" else \
            row.get("why") if row.get("kind") == "cancel_requested" else None
        if _line(words):
            entries.append((float(row["ts"]), "comment", _line(words)))
    out_dir = store.out_dir(job) if job.get("bot") else None
    final = out_dir / "final.md" if out_dir else None
    if final is not None and _first_line(final):
        entries.append((final.stat().st_mtime, "said", _first_line(final)))
    report = out_dir / "report.md" if out_dir else None
    told = (report.stat().st_mtime, _first_line(report)) if report is not None and _first_line(report) else (None, "")
    return entries, told


def _outcomes(kept: list[dict], job: dict, receipts: list[dict], told: tuple) -> dict[int, dict]:
    """``{index: {result, verdict}}``: the report's line goes to the round going when it was written, a verdict to
    the latest round that began before it; a round still going takes neither."""
    going = len(kept) - 1 if kept and kept[-1]["end"] is None and job.get("state") not in ENDED else None

    def latest_before(at: float) -> int | None:
        found = [i for i, one in enumerate(kept) if (_LOW if one["start"] is None else one["start"]) <= at]
        return found[-1] if found and found[-1] != going else None

    out: dict[int, dict] = {}
    if told[0] is not None and latest_before(told[0]) is not None:
        out.setdefault(latest_before(told[0]), {})["result"] = told[1]
    for row in receipts:
        if row.get("kind") == "verdict" and row.get("verdict") in ("accepted", "rejected"):
            index = latest_before(float(row["ts"]))
            if index is not None:
                out.setdefault(index, {})["verdict"] = row["verdict"]
    return out


def served_model(job: dict) -> dict | None:
    """The run's model as the client API serves it: ``{provider, id, thinking, source}``, None on the runner's own
    default. Provider and id are as they were written."""
    model = job.get("model")
    if not isinstance(model, dict):
        return None
    return {"provider": model.get("provider"), "id": model.get("id"), "thinking": model.get("thinking"),
            "source": model.get("source") or "runner"}


def settings_revision(job: dict) -> str | None:
    """The work-settings revision the run was submitted under (lina#257); None for a run from before."""
    settings = job.get("settings")
    rev = settings.get("rev") if isinstance(settings, dict) else None
    return str(rev) if rev else None


def card_id(job: dict) -> str | None:
    """The work card that started the run, by its id as the work API serves it (``hermes:<task>`` for a Hermes card,
    ``lina_board_hermes``); from the job's own ``card``, so no board is read. None when no card started it."""
    from hermes_cli.lina_board_hermes import hermes_id
    card = job.get("card")
    task = card.get("task") if isinstance(card, dict) else None
    if not task:
        return None
    return hermes_id(task) if kanban_link.on_hermes(card) else str(task)


def record(worker_root: Path, run_id: str, now: float) -> dict:
    """``{id, at, model, settingsRevision, cardId, rounds, comments, errors}``: rounds newest first, each with its rows oldest first; ``comments`` are
    the ones made while no round was going."""
    try:
        store = Store(worker_root)
        job = store.load(safe_id(run_id))
    except ValueError as exc:
        raise RunNotFound(run_id) from exc
    if job is None:
        raise RunNotFound(run_id)
    receipts = receipts_by_task(store).get(job["id"], [])
    kept = rounds(job, receipts)
    entries, told = _entries(store, job, receipts)
    errors = []
    if job.get("state") == "running" and job.get("host"):
        live, answered = _live(worker_root, job, now)
        entries += live
        if not answered:
            errors.append({"source": "devices", "message": f"{job['host']} did not answer: the round going now "
                                                           "shows only what the hub keeps"})
    placed: dict[int, list] = {index: [] for index in range(len(kept))}
    loose = []
    for entry in sorted(entries, key=lambda entry: entry[0]):
        home = round_at(kept, entry[0])
        if home is not None:
            placed[kept.index(home)].append(entry)
        elif entry[1] == "comment":
            loose.append(entry)
    outcomes = _outcomes(kept, job, receipts, told)
    served = []
    for index, one in enumerate(kept):
        outcome = outcomes.get(index, {})
        if one["start"] is None and not (placed[index] or outcome or one["end"] is not None):
            continue  # a round whose start fell out of the kept history is served only with something of its own
        served.append({"run": one["run"], "startedAt": one["start"], "endedAt": one["end"],
                       "endKnown": one["end"] is not None, "startUnknown": one["start"] is None,
                       "result": outcome.get("result"), "verdict": outcome.get("verdict"),
                       "stopped": one["end_state"] in STOPPED_ENDS, "rows": _fold(placed[index])})
    return {"id": job["id"], "at": now, "model": served_model(job), "settingsRevision": settings_revision(job),
            "cardId": card_id(job), "rounds": served[::-1], "comments": _fold(loose)[::-1], "errors": errors}
