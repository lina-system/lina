"""``lina brain proposals``: list an agent's proposals, accept or reject one, and read acceptance per phase.

Accepting applies a proposal only where its kind can be applied without losing text: a ``merge`` runs the recorded
merge of ``lina brain merge`` (no file changes), a ``close_edge`` sets the edge's ``valid_until`` (the row stays).
Any other kind (a new page, an addition to compiled truth, a contradiction) is marked ``accepted`` for the owner or a
later issue to carry out; nothing here writes a page file. A proposal graded ``external`` is accepted only with
``--confirm-external`` (lina#440).
"""

from __future__ import annotations

import argparse
import json

from hermes_cli.brain.dream.config import DreamError
from hermes_state_source_visibility import visible_atom, visible_ref

_COLUMNS = ("proposal_id", "phase", "kind", "target", "payload", "grade", "source_refs", "status", "cycle_id",
            "created_at", "decided_at", "decided_by")


def _apply_merge(conn, payload: dict) -> None:
    from hermes_cli.brain.store import merge_pages
    merge_pages(conn, payload["from"], payload["into"], payload.get("reason"))


def _apply_close_edge(conn, payload: dict) -> None:
    edge = payload["edge"]
    closed = conn.execute(
        "UPDATE brain.edges SET valid_until = current_date WHERE from_slug = %s AND to_slug = %s AND type = %s "
        "AND source_ref = %s AND valid_until IS NULL RETURNING 1",
        (edge["from"], edge["to"], edge["type"], edge["source_ref"])).fetchone()
    if closed is None:
        raise DreamError("the edge is gone or already closed; reject the proposal instead")


APPLY = {"merge": _apply_merge, "close_edge": _apply_close_edge}


def _visible_sources() -> str:
    return ("NOT EXISTS (SELECT 1 FROM unnest(source_refs) AS cited(ref) "
            f"WHERE NOT ({visible_ref('cited.ref')}) OR EXISTS (SELECT 1 FROM brain.atoms a "
            f"WHERE cited.ref = 'atom:' || a.atom_id AND NOT ({visible_atom('a.source_ref')})))")


def listing(conn, agent_id: str, status: str | None, phase: str | None) -> list[dict]:
    cur = conn.execute(
        f"SELECT {', '.join(_COLUMNS)} FROM brain.proposals WHERE agent_id = %s AND {_visible_sources()} "
        "AND (%s::text IS NULL OR status = %s) AND (%s::text IS NULL OR phase = %s) ORDER BY proposal_id",
        (agent_id, status, status, phase, phase))
    return [dict(zip(_COLUMNS, row)) for row in cur.fetchall()]


def decide(conn, agent_id: str, proposal_id: int, *, accept: bool, by: str, confirm_external: bool = False) -> str:
    """Record the decision in one transaction with what applying it changes; returns the new status."""
    with conn.transaction():
        row = conn.execute("SELECT kind, payload, grade, status FROM brain.proposals "
                           f"WHERE proposal_id = %s AND agent_id = %s AND {_visible_sources()} FOR UPDATE",
                           (proposal_id, agent_id)).fetchone()
        if row is None:
            raise DreamError(f"no proposal #{proposal_id} for agent {agent_id}")
        kind, payload, grade, status = row
        if status != "proposed":
            raise DreamError(f"proposal #{proposal_id} is already {status}")
        if not accept:
            status = "rejected"
        elif grade == "external" and not confirm_external:
            raise DreamError(f"proposal #{proposal_id} is built from external material; accept it with "
                             "--confirm-external once its source is confirmed")
        elif kind in APPLY:
            APPLY[kind](conn, payload)
            status = "applied"
        else:
            status = "accepted"
        conn.execute("UPDATE brain.proposals SET status = %s, decided_at = now(), decided_by = %s "
                     "WHERE proposal_id = %s", (status, by, proposal_id))
    return status


def rates(conn, agent_id: str) -> list[dict]:
    cur = conn.execute("SELECT phase, proposals, open, accepted, applied, rejected, acceptance_rate "
                       "FROM brain.proposal_acceptance WHERE agent_id = %s ORDER BY phase", (agent_id,))
    columns = [column.name for column in cur.description]
    return [dict(zip(columns, row)) for row in cur.fetchall()]


def _line(row: dict) -> str:
    payload = json.dumps(row["payload"], ensure_ascii=False)
    return (f"#{row['proposal_id']} {row['status']} {row['phase']}/{row['kind']} {row['target'] or '-'} "
            f"(grade {row['grade']}): {payload if len(payload) <= 160 else payload[:157] + '...'}")


def run(args: argparse.Namespace) -> int:
    from hermes_cli.brain.store import dumps, owner_connection
    from hermes_constants import get_hermes_home
    from hermes_state_store import agent_id_for
    agent_id = agent_id_for(get_hermes_home() / "state.db")
    with owner_connection() as conn:
        conn.autocommit = True
        if args.proposals_command == "list":
            rows = listing(conn, agent_id, None if args.status == "all" else args.status, args.phase)
            print(dumps({"proposals": rows}) if args.json else "\n".join(map(_line, rows)) or "No proposals.")
        elif args.proposals_command == "rates":
            rows = rates(conn, agent_id)
            if args.json:
                print(dumps({"phases": rows}))
                return 0
            if not rows:
                print("No proposals.")
            for row in rows:
                print(f"{row['phase']}: {row['accepted']} accepted ({row['applied']} applied), {row['rejected']} "
                      f"rejected, {row['open']} open; acceptance "
                      f"{'not measured' if row['acceptance_rate'] is None else row['acceptance_rate']}")
        else:
            status = decide(conn, agent_id, args.id, accept=args.proposals_command == "accept", by=args.by,
                            confirm_external=getattr(args, "confirm_external", False))
            print(f"Proposal #{args.id}: {status}.")
    return 0
