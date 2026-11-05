"""A run handed out from a work card: its start and its end are written on that card.

The card is named by ``submit --card <id> --board <slug>`` or by the card session's own environment
(``HERMES_KANBAN_TASK``, ``HERMES_KANBAN_BOARD``). Where it lives is ``worker.card_link``:

* unset or ``{kind: lina}``: LINA's own board, through the kanban DB API (``worker_run`` and ``worker_run_ended``
  events, the end notice as a comment).
* ``{kind: hermes, command: [hermes]}``: a Hermes Agent card, written only through that command's
  ``kanban [--board <b>] comment | attach | schedule | unblock``. No LINA code opens a kanban database in this mode.
  The run's start and end are comments, ``report.md`` is attached, and a card parked by ``lina worker wait``
  (``schedule``) is woken (``unblock``) once by the first end that follows.
"""

from __future__ import annotations

import contextlib
import os
import shlex
import subprocess
import time
from pathlib import Path

AUTHOR = "lina-worker"
HERMES = "hermes"
COMMAND_TIMEOUT_S = 120
# LINA's own home and the card session's identity must not reach Hermes's CLI: HERMES_HOME would point it at LINA's
# root, ``unblock`` refuses inside a card session (HERMES_KANBAN_TASK), and the author is always ``AUTHOR``.
_HUB_SIDE_DROP = ("HERMES_HOME", "HERMES_PROFILE", "HERMES_PROFILE_NAME")


class CardLinkError(RuntimeError):
    pass


def config(worker: dict) -> dict:
    """``worker.card_link`` as ``{kind: lina}`` or ``{kind: hermes, command: [...]}``."""
    link = worker.get("card_link")
    if link is None:
        return {"kind": "lina"}
    kind = link.get("kind") if isinstance(link, dict) else None
    if kind in (None, "lina"):
        return {"kind": "lina"}
    if kind != HERMES:
        raise CardLinkError(f"worker.card_link.kind is lina or hermes, not {kind!r}")
    command = link.get("command")
    if not isinstance(command, list) or not command or not all(isinstance(part, str) and part for part in command):
        raise CardLinkError("worker.card_link.command must be an argv list, e.g. [hermes]")
    return {"kind": HERMES, "command": command}


def card_from_env() -> dict | None:
    task = (os.environ.get("HERMES_KANBAN_TASK") or "").strip()
    if not task:
        return None
    return {"task": task, "board": (os.environ.get("HERMES_KANBAN_BOARD") or "").strip() or None}


def card_for(card: dict | None, link: dict) -> dict | None:
    """The card a run is recorded on, marked ``kind: hermes`` when it lives on a Hermes board."""
    if not card:
        return None
    return {**card, "kind": HERMES} if link["kind"] == HERMES else card


def on_hermes(card: dict | None) -> bool:
    return bool(card) and card.get("kind") == HERMES


def _hub_env() -> dict[str, str]:
    return {key: value for key, value in os.environ.items()
            if key not in _HUB_SIDE_DROP and not key.startswith("HERMES_KANBAN_")}


def _kanban(command: list[str], card: dict, verb: str, *args: str, env: dict[str, str] | None = None) -> None:
    argv = [*command, "kanban", *(["--board", card["board"]] if card.get("board") else []), verb, card["task"], *args]
    try:
        done = subprocess.run(argv, text=True, encoding="utf-8", errors="replace", capture_output=True,
                              env=_hub_env() if env is None else env, timeout=COMMAND_TIMEOUT_S, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise CardLinkError(f"{shlex.join(argv[:len(command) + 5])}: {exc}") from exc
    if done.returncode != 0:
        raise CardLinkError(f"{shlex.join(argv[:len(command) + 5])} exited {done.returncode}: "
                            f"{(done.stderr or done.stdout).strip()[-300:]}")


def _comment(command: list[str], card: dict, body: str) -> None:
    _kanban(command, card, "comment", "--author", AUTHOR, "--", body)


def _event(card: dict, kind: str, payload: dict) -> None:
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing, write_txn
    with connect_closing(board=card.get("board")) as conn, write_txn(conn):
        kb._require_task(conn, card["task"])
        kb._append_event(conn, card["task"], kind, payload)


def _start_line(job: dict) -> str:
    what = " · ".join(str(part) for part in (job.get("runner"), job.get("mode"), (job.get("model") or {}).get("id"),
                                             job.get("repo"), job.get("branch")) if part)
    return (f"[lina worker · automatic] run {job['id']} handed out ({what}). Its end comes as a comment here; "
            f"`lina worker wait` parks this card until then.")


def record_run(card: dict, job: dict, link: dict) -> None:
    if on_hermes(card):
        _comment(link["command"], card, _start_line(job))
        return
    _event(card, "worker_run", {"run_id": job["id"], "runner": job.get("runner"), "mode": job.get("mode"),
                                "model": job.get("model"), "repo": job.get("repo"), "branch": job.get("branch")})


def record_end(card: dict, job: dict, state: str, run: str, body: str, link: dict, report: Path | None = None) -> None:
    if on_hermes(card):
        _comment(link["command"], card, body)
        if report is not None and report.is_file():
            _kanban(link["command"], card, "attach", str(report), "--name", f"report-{job['id']}-{run}.md",
                    "--author", AUTHOR)
        return
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    _event(card, "worker_run_ended", {"run_id": job["id"], "runner": job.get("runner"), "state": state, "run": run,
                                      "device": job.get("host"), "delivery": job.get("delivery")})
    with connect_closing(board=card.get("board")) as conn:
        kb.add_comment(conn, card["task"], AUTHOR, body)


def card_key(card: dict) -> str:
    return "".join(ch if ch.isalnum() or ch in "-_" else "_" for ch in f"{card.get('board') or '-'}.{card['task']}")


def _parked_path(store, card: dict) -> Path:
    return store.root / "cards" / f"{card_key(card)}.parked"


def park(store, card: dict, link: dict, open_runs: list[str]) -> None:
    """Park the card (``kanban schedule``) from its own session, so its end of turn costs nothing until a run ends.

    Runs under the card's lock, which every end takes before it wakes the card: an end that lands while the card is
    being parked still finds it parked and wakes it."""
    with store.lock(f"card-{card_key(card)}"):
        mark = _parked_path(store, card)
        mark.parent.mkdir(parents=True, exist_ok=True)
        mark.write_text(" ".join(open_runs) + f"\n{time.time():.3f}\n", encoding="utf-8")
        try:
            _kanban(link["command"], card, "schedule", f"waiting on lina worker {' '.join(open_runs)}",
                    env=dict(os.environ))  # health: allow PS-P05 -- the card session's own env: Hermes ends its run
        except CardLinkError:
            mark.unlink(missing_ok=True)
            raise


def wake(store, card: dict, link: dict) -> bool:
    """Unblock the card once if ``park`` left it parked; True when it was."""
    with store.lock(f"card-{card_key(card)}"):
        mark = _parked_path(store, card)
        if not mark.exists():
            return False
        _kanban(link["command"], card, "unblock")
        with contextlib.suppress(FileNotFoundError):
            mark.unlink()
        return True
