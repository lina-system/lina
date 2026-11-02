"""A run started from inside a work card (``HERMES_KANBAN_TASK`` set): its ID and runner go on that card as an event
(``worker_run``), its end as ``worker_run_ended`` plus the end notice as a comment, through the kanban DB API.
"""

from __future__ import annotations

import os

AUTHOR = "lina-worker"


def card_from_env() -> dict | None:
    task = (os.environ.get("HERMES_KANBAN_TASK") or "").strip()
    if not task:
        return None
    return {"task": task, "board": (os.environ.get("HERMES_KANBAN_BOARD") or "").strip() or None}


def _event(card: dict, kind: str, payload: dict) -> None:
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing, write_txn
    with connect_closing(board=card.get("board")) as conn, write_txn(conn):
        kb._require_task(conn, card["task"])
        kb._append_event(conn, card["task"], kind, payload)


def record_run(card: dict, job: dict) -> None:
    _event(card, "worker_run", {"run_id": job["id"], "runner": job.get("runner"), "mode": job.get("mode"),
                                "model": job.get("model"), "repo": job.get("repo"), "branch": job.get("branch")})


def record_end(card: dict, job: dict, state: str, run: str, body: str) -> None:
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    _event(card, "worker_run_ended", {"run_id": job["id"], "runner": job.get("runner"), "state": state, "run": run,
                                      "device": job.get("host"), "delivery": job.get("delivery")})
    with connect_closing(board=card.get("board")) as conn:
        kb.add_comment(conn, card["task"], AUTHOR, body)
