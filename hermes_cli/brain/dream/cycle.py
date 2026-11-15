"""One run of ``lina brain dream``: deterministic phases first (upkeep, orphans: no model, no cycle lock), then the
model phases under one lock with a TTL, each spending from its own budget; then the counters, a one-line report and
a report page in the configured folder (never inside an imported wiki).

``--plan`` builds every request and checks it against the budget and the answered inputs, but calls no model and
writes nothing. An input a phase already answered is not sent again, so an unchanged brain costs nothing.
"""

from __future__ import annotations

import argparse
import logging
import os
import uuid
from collections import Counter
from datetime import timedelta
from pathlib import Path

from hermes_cli.brain.dream.config import DETERMINISTIC, MODEL_PHASES, PHASES, DreamError, Settings
from hermes_cli.brain.dream.model import Complete, Invalid, Spend, answer_object, digest
from hermes_cli.brain.dream.phases import PHASE_CLASSES, Atoms, Phase, Proposal

LOCK = "dream"
logger = logging.getLogger(__name__)


class Lock:
    """The cycle lock, a row of ``brain.cycle_lock``: taken when free or expired, kept alive between model calls."""

    def __init__(self, holder: str, ttl: timedelta) -> None:
        self.holder, self.ttl = holder, ttl

    def acquire(self, conn) -> str | None:
        """None when taken; else who holds it and until when."""
        taken = conn.execute(
            "INSERT INTO brain.cycle_lock (name, holder, expires_at) VALUES (%s, %s, now() + %s) "
            "ON CONFLICT (name) DO UPDATE SET holder = EXCLUDED.holder, acquired_at = now(), "
            "expires_at = EXCLUDED.expires_at WHERE brain.cycle_lock.expires_at < now() RETURNING holder",
            (LOCK, self.holder, self.ttl)).fetchone()
        if taken:
            return None
        holder, until = conn.execute("SELECT holder, expires_at FROM brain.cycle_lock WHERE name = %s",
                                     (LOCK,)).fetchone()
        return f"locked by {holder} until {until.isoformat(timespec='seconds')}"

    def refresh(self, conn) -> bool:
        return conn.execute("UPDATE brain.cycle_lock SET expires_at = now() + %s WHERE name = %s AND holder = %s "
                            "RETURNING 1", (self.ttl, LOCK, self.holder)).fetchone() is not None

    def release(self, conn) -> None:
        conn.execute("DELETE FROM brain.cycle_lock WHERE name = %s AND holder = %s", (LOCK, self.holder))


def _write(conn, writes: list[Atoms | Proposal], phase: str, agent_id: str, cycle_id: int, counts: Counter,
           ids: list[int]) -> None:
    from psycopg.types.json import Jsonb
    for write in writes:
        if isinstance(write, Atoms):
            conn.execute("DELETE FROM brain.atoms WHERE page_slug = %s", (write.slug,))
            for claim, grade, ref in write.claims:
                conn.execute("INSERT INTO brain.atoms (page_slug, claim, grade, source_ref) VALUES (%s, %s, %s, %s)",
                             (write.slug, claim, grade, ref))
            counts["atoms"] += len(write.claims)
            continue
        row = conn.execute(
            "INSERT INTO brain.proposals (agent_id, phase, kind, target, payload, grade, source_refs, cycle_id, "
            "fingerprint) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s) ON CONFLICT (agent_id, fingerprint) DO NOTHING "
            "RETURNING proposal_id",
            (agent_id, phase, write.kind, write.target, Jsonb(write.payload), write.grade,
             list(write.source_refs), cycle_id, write.fingerprint)).fetchone()
        if row:
            counts["proposals"] += 1
            ids.append(row[0])
        else:
            counts["repeated"] += 1


def _answered(conn, agent_id: str, phase: str) -> dict[str, str]:
    return dict(conn.execute("SELECT subject, digest FROM brain.dream_inputs WHERE agent_id = %s AND phase = %s",
                             (agent_id, phase)).fetchall())


def plan_phase(conn, name: str, settings: Settings, agent_id: str) -> dict:
    phase: Phase = PHASE_CLASSES[name].loaded(conn, settings, agent_id)
    answered = _answered(conn, agent_id, name)
    spend = Spend(settings.budgets[name])
    counts = Counter()
    for request in phase.requests():
        text = request.prompt
        if answered.get(request.subject) == digest(text):
            counts["unchanged"] += 1
        elif spend.fits(text):
            spend.calls += 1
            spend.input_chars += len(text)
        else:
            counts["over_budget"] += 1
        counts["requests"] += 1
    return {"requests": counts["requests"], "unchanged": counts["unchanged"], "would_call": spend.calls,
            "would_send_chars": spend.input_chars, "over_budget": counts["over_budget"],
            "budget": spend.as_json()["budget"], "stopped": spend.stopped}


def run_phase(conn, name: str, settings: Settings, complete: Complete, agent_id: str, cycle_id: int,
              lock: Lock) -> dict:
    """Send the phase's requests until its budget is spent; write what each valid answer yields."""
    phase: Phase = PHASE_CLASSES[name].loaded(conn, settings, agent_id)
    answered = _answered(conn, agent_id, name)
    spend = Spend(settings.budgets[name])
    counts: Counter = Counter()
    ids: list[int] = []
    invalid: list[str] = []
    failed = None
    for request in phase.requests():
        text = request.prompt
        key = digest(text)
        if answered.get(request.subject) == key:
            counts["unchanged"] += 1
            continue
        if not lock.refresh(conn):
            failed = "the cycle lock was lost"
            break
        try:
            answer = spend.ask(complete, text)
        except Exception as error:  # any provider error: reported, ends this phase only, the run exits 1
            logger.exception("brain dream: the %s model call for %s failed", name, request.subject)
            failed = f"model call failed: {type(error).__name__}: {error}"
            break
        if answer is None:
            break
        try:
            writes = phase.read(request, answer_object(answer))
        except Invalid as error:
            counts["invalid"] += 1
            invalid.append(f"{request.subject}: {error}")
            continue
        with conn.transaction():
            _write(conn, writes, name, agent_id, cycle_id, counts, ids)
            conn.execute("INSERT INTO brain.dream_inputs (agent_id, phase, subject, digest, cycle_id) "
                         "VALUES (%s, %s, %s, %s, %s) ON CONFLICT (agent_id, phase, subject) DO UPDATE "
                         "SET digest = EXCLUDED.digest, cycle_id = EXCLUDED.cycle_id",
                         (agent_id, name, request.subject, key, cycle_id))
        counts["answered"] += 1
    if failed is None:
        with conn.transaction():
            _write(conn, phase.finish(), name, agent_id, cycle_id, counts, ids)
    return {**spend.as_json(), "answered": counts["answered"], "unchanged": counts["unchanged"],
            "invalid": counts["invalid"], "invalid_answers": invalid, "atoms": counts["atoms"],
            "proposals": counts["proposals"], "repeated": counts["repeated"], "proposal_ids": ids, "failed": failed}


def orphans(conn) -> list[str]:
    return [row[0] for row in conn.execute(
        "SELECT p.slug FROM brain.pages AS p WHERE NOT EXISTS (SELECT 1 FROM brain.edges AS e "
        "WHERE e.from_slug = p.slug OR e.to_slug = p.slug) ORDER BY p.slug")]


def _upkeep(conn, plan: bool) -> dict:
    from hermes_cli.brain.upkeep import _root_report
    roots = conn.execute("SELECT root_id, path, mapping FROM brain.roots ORDER BY path").fetchall()
    if plan:
        return {"roots": len(roots), "planned": True}
    reports = [_root_report(conn, root) for root in roots]
    return {"roots": len(roots), "new_links": sum(r["new_links"] for r in reports),
            "fixed_backlinks": sum(r["fixed_backlinks"] for r in reports),
            "lint_findings": sum(len(r["lint"]) for r in reports),
            "missing_backlinks": sum(len(r["missing_backlinks"]) for r in reports),
            "unindexed_pages": sum(len(r["unindexed_pages"]) for r in reports)}


def _check_report_dir(conn, settings: Settings) -> Path:
    folder = settings.report_dir.resolve()
    for (root,) in conn.execute("SELECT path FROM brain.roots"):
        if folder == Path(root) or Path(root) in folder.parents:
            raise DreamError(f"brain.dream.report_dir {folder} is inside the imported wiki {root}; "
                             "the report goes to a work folder outside it")
    return folder


def _counters(conn, agent_id: str, cycle_id: int) -> dict:
    lonely = set(orphans(conn))
    pages = conn.execute("SELECT count(*) FROM brain.pages").fetchone()[0]
    new = [row[0] for row in conn.execute(
        "INSERT INTO brain.dream_pages_seen (agent_id, slug, first_cycle_id) SELECT %s, slug, %s FROM brain.pages "
        "ON CONFLICT DO NOTHING RETURNING slug", (agent_id, cycle_id))]
    return {"pages": pages, "orphans": len(lonely), "new_pages": len(new),
            "new_pages_linked": sum(slug not in lonely for slug in new)}


def _model_phases(conn, names: list[str], settings: Settings, complete: Complete, agent_id: str,
                  cycle_id: int) -> tuple[dict, str | None]:
    lock = Lock(f"{agent_id}:{os.getpid()}:{uuid.uuid4().hex[:8]}", settings.lock_ttl)
    held = lock.acquire(conn)
    if held:
        return {name: {"skipped": held} for name in names}, held
    try:
        return {name: run_phase(conn, name, settings, complete, agent_id, cycle_id, lock) for name in names}, None
    finally:
        lock.release(conn)


def run_cycle(conn, complete: Complete, settings: Settings, agent_id: str, *, plan: bool = False,
              only: str | None = None) -> dict:
    """One cycle on an autocommit owner connection; returns the report (``line`` is the one-line form)."""
    names = [only] if only else list(PHASES)
    report_dir = _check_report_dir(conn, settings)
    cycle_id = None if plan else conn.execute(
        "INSERT INTO brain.dream_cycles (agent_id) VALUES (%s) RETURNING cycle_id", (agent_id,)).fetchone()[0]
    phases: dict[str, dict] = {}
    if "upkeep" in names:
        phases["upkeep"] = _upkeep(conn, plan)
    if "orphans" in names:
        phases["orphans"] = {"orphans": orphans(conn)}
    model = [name for name in names if name in MODEL_PHASES]
    lock_note = None
    if plan:
        phases.update({name: plan_phase(conn, name, settings, agent_id) for name in model})
    elif model:
        done, lock_note = _model_phases(conn, model, settings, complete, agent_id, cycle_id)
        phases.update(done)
    report = {"cycle_id": cycle_id, "agent_id": agent_id, "plan": plan,
              "date": conn.execute("SELECT current_date").fetchone()[0].isoformat(), "phases": phases,
              "lock": lock_note}
    if plan:
        report["line"] = plan_line(report)
        return report
    report.update(_counters(conn, agent_id, cycle_id))
    report["spend"] = {key: sum(phases[name].get(key, 0) for name in model)
                       for key in ("calls", "input_chars", "output_chars")}
    report["line"] = line(report)
    report["report_page"] = str(write_page(report_dir, report))
    from psycopg.types.json import Jsonb
    conn.execute(
        "UPDATE brain.dream_cycles SET finished_at = now(), pages = %s, orphans = %s, new_pages = %s, "
        "new_pages_linked = %s, new_links = %s, calls = %s, input_chars = %s, output_chars = %s, report = %s "
        "WHERE cycle_id = %s",
        (report["pages"], report["orphans"], report["new_pages"], report["new_pages_linked"],
         phases.get("upkeep", {}).get("new_links", 0), report["spend"]["calls"], report["spend"]["input_chars"],
         report["spend"]["output_chars"], Jsonb(report), cycle_id))
    return report


def _stops(phases: dict) -> list[str]:
    notes = []
    for name in MODEL_PHASES:
        result = phases.get(name) or {}
        if result.get("failed"):
            notes.append(f"{name} failed ({result['failed']})")
        elif result.get("stopped"):
            notes.append(f"{name} stopped at its {result['stopped']} budget")
    return notes


def line(report: dict) -> str:
    phases = report["phases"]
    proposals = ", ".join(f"{name} {phases[name].get('proposals', 0)}" for name in MODEL_PHASES[1:] if name in phases)
    contradictions = (phases.get("contradictions") or {}).get("proposals", 0)
    spend = report["spend"]
    parts = [f"Dream cycle {report['cycle_id']}: {phases.get('upkeep', {}).get('new_links', 0)} new links",
             f"{(phases.get('atoms') or {}).get('atoms', 0)} atoms",
             f"proposals {proposals or 'none'}", f"{contradictions} contradictions",
             f"{report['orphans']} of {report['pages']} pages orphaned",
             f"spent {spend['calls']} calls, {spend['input_chars']} input chars"]
    if report["lock"]:
        parts.append(f"model phases skipped: {report['lock']}")
    parts += _stops(phases)
    return "; ".join(parts) + "."


def plan_line(report: dict) -> str:
    phases = report["phases"]
    calls = ", ".join(f"{name} {phases[name]['would_call']}" + (f" (then {phases[name]['stopped']} budget)"
                                                               if phases[name]["stopped"] else "")
                      for name in MODEL_PHASES if name in phases)
    upkeep = phases.get("upkeep")
    head = f"Dream plan: upkeep over {upkeep['roots']} roots" if upkeep else "Dream plan"
    return f"{head}; would call {calls or 'no model phase'}; no model called, nothing written."


def write_page(folder: Path, report: dict) -> Path:
    folder.mkdir(parents=True, exist_ok=True)
    path = folder / f"{report['date']}-cycle-{report['cycle_id']}.md"
    rows = []
    for name in PHASES:
        result = report["phases"].get(name)
        if result is None or name in DETERMINISTIC:
            continue
        rows.append(f"| {name} | {result.get('calls', 0)} | {result.get('input_chars', 0)} | "
                    f"{result.get('answered', 0)} | {result.get('unchanged', 0)} | {result.get('invalid', 0)} | "
                    f"{result.get('atoms', 0)} | {result.get('proposals', 0)} | "
                    f"{result.get('failed') or result.get('skipped') or result.get('stopped') or ''} |")
    upkeep = report["phases"].get("upkeep") or {}
    lonely = (report["phases"].get("orphans") or {}).get("orphans", [])
    lines = [f"# Dream cycle {report['cycle_id']} ({report['date']})", "", report["line"], "",
             f"- Agent: {report['agent_id']}",
             f"- Upkeep: {upkeep.get('new_links', 0)} new links, {upkeep.get('fixed_backlinks', 0)} fixed backlinks, "
             f"{upkeep.get('lint_findings', 0)} lint findings over {upkeep.get('roots', 0)} roots",
             f"- Pages: {report['pages']}, orphans {report['orphans']}, first seen this cycle {report['new_pages']} "
             f"(linked {report['new_pages_linked']})",
             "", "| Phase | Calls | Input chars | Answered | Unchanged | Invalid | Atoms | Proposals | Note |",
             "| --- | --- | --- | --- | --- | --- | --- | --- | --- |", *rows, ""]
    ids = [n for name in MODEL_PHASES for n in (report["phases"].get(name) or {}).get("proposal_ids", [])]
    if ids:
        lines += ["New proposals (`lina brain proposals list`): " + ", ".join(f"#{n}" for n in ids), ""]
    if lonely:
        lines += ["## Orphans", "", *(f"- {slug}" for slug in lonely), ""]
    path.write_text("\n".join(lines), encoding="utf-8")
    return path


def run(args: argparse.Namespace) -> int:
    from hermes_cli.brain.dream import model
    from hermes_cli.brain.dream.config import load as load_settings
    from hermes_cli.brain.store import dumps, owner_connection
    from hermes_constants import get_hermes_home
    from hermes_state_store import agent_id_for
    home = get_hermes_home()
    settings = load_settings(home)
    agent_id = agent_id_for(home / "state.db")
    with owner_connection() as conn:
        conn.autocommit = True
        if args.metrics:
            return _print_metrics(conn, agent_id, args.json)
        report = run_cycle(conn, model.aux_complete, settings, agent_id, plan=args.plan, only=args.phase)
    if args.json:
        print(dumps(report))
    else:
        print(report["line"])
        if report.get("report_page"):
            print(f"Report: {report['report_page']}")
    return 1 if any((report["phases"].get(name) or {}).get("failed") for name in MODEL_PHASES) else 0


def _print_metrics(conn, agent_id: str, as_json: bool) -> int:
    from hermes_cli.brain.store import dumps
    cur = conn.execute("SELECT * FROM brain.dream_metrics WHERE agent_id = %s ORDER BY cycle_id", (agent_id,))
    columns = [column.name for column in cur.description]
    rows = [dict(zip(columns, row)) for row in cur.fetchall()]
    if as_json:
        print(dumps({"cycles": rows}))
        return 0
    for row in rows:
        share = "no new pages" if row["new_linked_share"] is None else row["new_linked_share"]
        print(f"Cycle {row['cycle_id']} ({row['started_at']:%Y-%m-%d}): orphan rate {row['orphan_rate']} "
              f"({row['orphans']} of {row['pages']}), new pages linked {row['new_pages_linked']} of "
              f"{row['new_pages']} ({share}), {row['new_links']} new links, {row['calls']} calls, "
              f"{row['input_chars']} input chars, {row['proposals']} proposals.")
    if not rows:
        print("No finished dream cycles.")
    return 0
