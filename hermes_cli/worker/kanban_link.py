"""A run handed out from a work card: its start and its end are written on that card.

The card is named by ``submit --card <id> --board <slug>`` or by the card session's own environment
(``HERMES_KANBAN_TASK``, ``HERMES_KANBAN_BOARD``) of this process, else of its nearest ancestor that has it: Hermes
removes both from a card session's terminal, so the card's worker process above it is the usual source. The card
is only read here. Where it lives is ``worker.card_link``:

* unset or ``{kind: lina}``: LINA's own board, through the kanban DB API (``worker_run``, ``worker_run_ended`` and
  ``worker_host_unavailable`` events, each carrying its ``events`` kind, the end notice as a comment).
* ``{kind: hermes, command: [/path/to/hermes]}``: a Hermes Agent card, written only through that command's
  ``kanban [--board <b>] comment | attach | show | unblock``. No LINA code opens a kanban database in this mode.
  The hub writes start/end comments and attaches ``report.md``. The card's model schedules itself
  (``kanban_schedule``), naming the runs it waits on in the reason or in ``workers``; ``lina worker wait`` also
  records them. The hub wakes it (``unblock --judge-wake <run>``, bare ``unblock`` on a Hermes without that option)
  once on a run's end (``wake``). The end is three steps
  (``END_STEPS``); the hub keeps the ones that went through and retries the rest.
"""

from __future__ import annotations

import contextlib
import json
import os
import re
import shlex
import subprocess
import sys
import time
from collections.abc import Callable
from pathlib import Path

from hermes_cli.worker import events, kanban_link_wait

AUTHOR = "lina-worker"
HERMES = "hermes"
COMMAND_TIMEOUT_S = 120
LEGACY_STATUS_TIMEOUT_S = 5
# LINA's own home and the card session's identity must not reach Hermes's CLI: HERMES_HOME would point it at LINA's
# root, ``unblock`` refuses inside a card session (HERMES_KANBAN_TASK), and the author is always ``AUTHOR``.
_HUB_SIDE_DROP = ("HERMES_HOME", "HERMES_PROFILE", "HERMES_PROFILE_NAME")
ANCESTOR_LEVELS = 16
# A run or batch ID as a whole token: not inside a longer word, and not the head of ``<ID>-<n>`` (another run).
RUN_ID = re.compile(r"(?<![A-Za-z0-9_])(?<![A-Za-z0-9]-)(O[TG]-[A-Z0-9]{1,3}[0-9]{10}(?:-[0-9]+)?)(?![A-Za-z0-9_])"
                    r"(?!-[A-Za-z0-9])")


class CardLinkError(RuntimeError):
    pass


class CardWaiting(CardLinkError):
    """The card is not ready for this step yet (it has not scheduled itself, or still waits on other runs): a wait,
    not a failing link (``kanban_link_wait``), with its *reason* (``card running``, ``other runs open``)."""

    def __init__(self, message: str, reason: str):
        super().__init__(message)
        self.reason = reason


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
        raise CardLinkError("worker.card_link.command must be an argv list, e.g. [/path/to/hermes]")
    from hermes_platform.resolver import locate_command
    found = next(iter(locate_command(command[0]).command), None)
    if found and _in_lina_tree(found):
        raise CardLinkError(f"worker.card_link.command[0] {command[0]!r} is this LINA's own {found}; "
                            "set the Hermes Agent CLI by absolute path")
    return {"kind": HERMES, "command": [found or command[0], *command[1:]],
            "goal_drive": worker.get("goal_drive") is not False}


def lina_trees() -> list[Path]:
    """The running LINA's own trees: the checkout (or install directory) holding the ``hermes_cli`` package, whose
    ``.hermes/bin/hermes`` is LINA's alias, and the virtual environment it runs in, whose ``bin/hermes`` is its
    console script. A Python that is not a virtual environment (``sys.prefix == sys.base_prefix``) is a shared
    system prefix, not LINA's."""
    import hermes_cli
    trees = [Path(hermes_cli.__file__).resolve().parent.parent]
    if sys.prefix != sys.base_prefix:
        trees.append(Path(sys.prefix).resolve())
    return trees


def _in_lina_tree(path: str) -> bool:
    """Whether *path*, with its links followed, is a file of the running LINA, or a user-bin forwarder to one
    (``exec <tree>/.hermes/bin/hermes "$@"``). The interpreter itself is not LINA's ``hermes`` (``[python, script]``
    runs the script), even where a virtual environment copies it in."""
    from hermes_cli._launchers import _owns_launcher
    real = Path(path).resolve()
    if real == Path(sys.executable).resolve():
        return False
    return any(real.is_relative_to(tree) or _owns_launcher(Path(path), tree) for tree in lina_trees())


def _card_of(env) -> dict | None:
    task = (env.get("HERMES_KANBAN_TASK") or "").strip()
    if not task:
        return None
    return {"task": task, "board": (env.get("HERMES_KANBAN_BOARD") or "").strip() or None}


def card_from_env() -> dict | None:
    """The card of this process's environment, else of its nearest ancestor's."""
    return _card_of(os.environ) or card_from_ancestors()


def proc_env(pid: int) -> dict[str, str]:
    """The environment *pid* was started with; {} when it cannot be read. Linux: ``/proc/<pid>/environ``. macOS:
    ``sysctl KERN_PROCARGS2`` (Apple-signed binaries hide theirs; the Hermes Python shows it)."""
    raw = _procargs(pid)
    env: dict[str, str] = {}
    for part in raw:
        key, sep, value = part.partition(b"=")
        if sep and key:
            env[key.decode(errors="replace")] = value.decode(errors="replace")
    return env


def _procargs(pid: int) -> list[bytes]:
    try:
        return Path(f"/proc/{pid}/environ").read_bytes().split(b"\0")
    except OSError:
        pass
    if sys.platform != "darwin":
        return []
    import ctypes
    import ctypes.util
    import struct
    libc = ctypes.CDLL(ctypes.util.find_library("c"), use_errno=True)
    mib = (ctypes.c_int * 3)(1, 49, int(pid))  # CTL_KERN, KERN_PROCARGS2
    size = ctypes.c_size_t(0)
    if libc.sysctl(mib, 3, None, ctypes.byref(size), None, 0) != 0 or not size.value:
        return []
    buf = ctypes.create_string_buffer(size.value)
    if libc.sysctl(mib, 3, buf, ctypes.byref(size), None, 0) != 0:
        return []
    raw = buf.raw[:size.value]
    argc, rest = struct.unpack_from("i", raw, 0)[0], raw[4:]
    start = rest.find(b"\0")  # the exec path, its NUL padding, argv, then the environment
    while 0 <= start < len(rest) and rest[start:start + 1] == b"\0":
        start += 1
    parts = rest[start:].split(b"\0")[argc:]
    return parts[:parts.index(b"")] if b"" in parts else parts


def parent_pid(pid: int) -> int:
    try:
        return int(Path(f"/proc/{pid}/stat").read_text(encoding="utf-8-sig").rsplit(")", 1)[1].split()[1])
    except (OSError, IndexError, ValueError):
        pass
    try:
        done = subprocess.run(["ps", "-o", "ppid=", "-p", str(pid)], capture_output=True, text=True, encoding="utf-8",
                              errors="replace", timeout=5, check=False)
        return int(done.stdout.strip() or 0)
    except (OSError, ValueError, subprocess.TimeoutExpired):
        return 0


def card_from_ancestors(pid: int | None = None, *, env_of: Callable[[int], dict] = proc_env,
                        parent_of: Callable[[int], int] = parent_pid) -> dict | None:
    """The card in the environment of the nearest ancestor of this process (from its parent, up to
    ``ANCESTOR_LEVELS``) that has ``HERMES_KANBAN_TASK``; its board from the same environment."""
    pid = os.getppid() if pid is None else pid
    for _ in range(ANCESTOR_LEVELS):
        if pid <= 1:
            return None
        if card := _card_of(env_of(pid)):
            return card
        pid = parent_of(pid)
    return None


def card_for(card: dict | None, link: dict) -> dict | None:
    """The card a run is recorded on, marked ``kind: hermes`` when it lives on a Hermes board."""
    if not card:
        return None
    return {**card, "kind": HERMES} if link["kind"] == HERMES else card


def on_hermes(card: dict | None) -> bool:
    return bool(card) and card.get("kind") == HERMES


def judged_cards(card: dict) -> list[dict]:
    """The ``cards`` of a run on a Hermes card, in the shape Hermes reads from a job record to refuse the card's
    ``complete`` / ``request-review`` while the run it launched is unjudged."""
    return [{"id": card["task"], "board": card.get("board") or "default"}]


def linked_cards(job: dict) -> list[dict]:
    """Notification cards; old jobs have only the submit-time ``card``."""
    return job.get("card_links", [job["card"]] if job.get("card") else [])


def change_card(hub, task_id: str, card: str | None, drop: str | None, board: str | None) -> int:
    """Change links under the same lock as end delivery; no-op calls never save the job."""
    from hermes_cli.worker.hub import HubError
    if card and drop:
        raise HubError("card: choose a card to add or --drop <card>, not both")
    if board and not card:
        raise HubError("--board names the board of the card to add")
    with hub.store.lock(task_id):
        job = hub.need(task_id)
        linked = linked_cards(job)
        if not card and not drop:
            for item in linked:
                hub.out(f"{item['task']}  {item.get('board') or '-'}  ({item.get('kind') or 'lina'})")
            if not linked:
                hub.out(f"{task_id}: no card")
            return 0
        existing = next((item for item in linked if item["task"] == (card or drop)), None)
        if (card and existing) or (drop and not existing):
            hub.out(f"{task_id}: card {card or drop} " + ("already linked" if card else "not linked"))
            return 0
        changed = card_for({"task": card, "board": board or os.environ.get("HERMES_KANBAN_BOARD") or None},
                           hub._link()) if card else existing
        links = [*linked, changed] if card else [item for item in linked if item != existing]
        job["card_links"] = links
        job["cards"] = [entry for item in links if on_hermes(item) for entry in judged_cards(item)]
        job["card"] = links[0] if links else None
        if drop and existing == (linked[0] if linked else None):
            job.pop("card_start_pending", None)
        hub.store.save(job)
        hub.store.receipt(task_id, "card_linked" if card else "card_dropped", card=changed)
        hub.out(f"{task_id}: card {card or drop} " + ("linked" if card else "dropped"))
    return 0


def end_cards(hub, job: dict, entry: dict, *, ticked: bool = False) -> str:
    """Advance each remaining card independently, retaining completed steps across retries; a wake that waits is
    tried by a tick only past its gap (*ticked*)."""
    targets = entry.setdefault("cards", linked_cards(job))
    progress = entry.setdefault("card_progress", {})
    pending = ""
    for card in targets:
        if card not in linked_cards(job):
            continue
        key = json.dumps(card, sort_keys=True)
        state = progress.setdefault(key, {"done": list(entry["done"])})
        if state.get("noticed"):
            continue
        for step in END_STEPS:
            if step in state["done"]:
                continue
            if step == "wake" and (gap := kanban_link_wait.gate(hub, job, entry, card, state,
                                                                          ticked=ticked)) is not None:
                if gap:
                    pending = gap
                    break
                state["done"].append(step)  # expired: noticed, not woken
                hub.store.save(job)
                continue
            try:
                if on_hermes(card):
                    woke = end_step(step, hub.store, card, job, entry["run"],
                                    events.signed(entry["text"], entry.get("delivery")), config(hub.worker),
                                    Path(entry["report"]) if entry.get("report") else None,
                                    drive=hub.worker.get("goal_drive") is not False and card == job.get("card"),
                                    question=entry["kind"] == "human")
                else:
                    woke = None
                    if step == "comment":
                        record_end(card, job, entry["kind"], entry["run"],
                                   events.signed(entry["text"], entry.get("delivery")))
            except CardWaiting as exc:
                pending = kanban_link_wait.waited(hub, job, card, state, exc)
                hub._card_warning(job, state, card)
                break
            except (OSError, ValueError, CardLinkError) as exc:
                hub.store.receipt(job["id"], "card_notice_failed", card=card, step=step, error=str(exc)[:300])
                hub._card_failed(job, exc)
                pending = f"card {card['task']}: {step} failed, retried on the next report or tick"
                break
            state["done"].append(step)
            job.pop("card_link_failing", None)
            if woke:
                state["woke"], state["unblock"] = True, woke
            hub.store.save(job)
            hub._card_warning(job, state, card)
        if any(step not in state["done"] for step in END_STEPS):
            continue
        if not hub._card_warning(job, state, card):
            pending = f"card {card['task']}: the card-link warning failed, retried on the next report or tick"
            continue
        hub.store.receipt(job["id"], "card_noticed", card=card, state=entry["kind"], run=entry["run"],
                          woke=bool(state.get("woke")), unblock=state.get("unblock"))
        state["noticed"] = True
        hub.store.save(job)
    return pending


def hub_env() -> dict[str, str]:
    return {key: value for key, value in os.environ.items()
            if key not in _HUB_SIDE_DROP and not key.startswith("HERMES_KANBAN_")}


def _run(command: list[str], card: dict, verb: str, *args: str, options: tuple[str, ...] = ()) -> tuple[
        str, subprocess.CompletedProcess]:
    argv = [*command, "kanban", *(["--board", card["board"]] if card.get("board") else []), verb, *options,
            card["task"], *args]
    shown = shlex.join(argv[:len(command) + 5 + len(options)])
    try:
        return shown, subprocess.run(argv, text=True, encoding="utf-8", errors="replace", capture_output=True,
                                     env=hub_env(), timeout=COMMAND_TIMEOUT_S, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise CardLinkError(f"{shown}: {exc}") from exc


def _checked(shown: str, done: subprocess.CompletedProcess) -> str:
    if done.returncode != 0:
        raise CardLinkError(f"{shown} exited {done.returncode}: {(done.stderr or done.stdout).strip()[-300:]}")
    return done.stdout


def _kanban(command: list[str], card: dict, verb: str, *args: str) -> str:
    return _checked(*_run(command, card, verb, *args))


def unblock(command: list[str], card: dict, run_id: str, reason: str | None = None) -> str:
    """Unblock the card for judging *run_id*; the form used, ``judge_wake`` or ``bare``.

    ``--judge-wake`` lands the card ``ready`` even behind an open parent, so it can judge the run. A Hermes without
    that option (argparse: exit 2, "unrecognized arguments") gets the bare ``unblock`` once."""
    args = ("--reason", reason) if reason is not None else ()
    shown, done = _run(command, card, "unblock", *args, options=("--judge-wake", run_id))
    if done.returncode == 2 and ("unrecognized arguments" in done.stderr or "--judge-wake" in done.stderr):
        _kanban(command, card, "unblock", *args)
        return "bare"
    _checked(shown, done)
    return "judge_wake"


def comment(command: list[str], card: dict, body: str) -> None:
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
            "use `lina worker wait --card <id> --board <slug>` and follow its kanban_schedule instruction.")


def record_run(card: dict, job: dict, link: dict) -> None:
    if on_hermes(card):
        comment(link["command"], card, _start_line(job))
        return
    _event(card, "worker_run", {"run_id": job["id"], "runner": job.get("runner"), "mode": job.get("mode"),
                                "model": job.get("model"), "repo": job.get("repo"), "branch": job.get("branch")})


END_STEPS = ("comment", "attach", "wake")


def end_step(step: str, store, card: dict, job: dict, run: str, body: str, link: dict,
             report: Path | None, *, drive: bool = False, question: bool = False) -> str | None:
    """One step of a run's end on a Hermes card; raises ``CardLinkError`` when it did not go through. The unblock
    form (``judge_wake`` / ``bare``) when ``wake`` woke the card."""
    if step == "comment":
        comment(link["command"], card, body)
    elif step == "attach" and report is not None and report.is_file():
        _kanban(link["command"], card, "attach", str(report), "--name", f"report-{job['id']}-{run}.md",
                "--author", AUTHOR)
    elif step == "wake":
        return wake(store, card, link, job["id"], judged=job.get("group") or job["id"],
                    settled=settled(job, run), drive=drive, question=body if question else None)
    return None


def settled(job: dict, run: str) -> bool:
    """Whether the end of *run* is no longer one the card can park on: the run has its verdict, or a later run of
    the task superseded it."""
    later = str(run).isdigit() and int(run) < int(job.get("runs") or 1)
    return job.get("verdict") in ("accepted", "rejected") or later


def _on_hub(store, run_id: str) -> bool:
    """Whether this hub owns *run_id* (a run record, or an ``OG-`` batch with members): its end wakes the card."""
    if run_id.startswith("OG-"):
        return any(job.get("group") == run_id for job in store.all())
    return store.load(run_id) is not None


def record_end(card: dict, job: dict, state: str, run: str, body: str) -> None:
    """The end (or question, ``state: human``) of a run on a card of LINA's own board; a card that waits
    is woken to judge it (``kanban_db_verdicts.wake_for_run``)."""
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    from hermes_cli.kanban_db_verdicts import wake_for_run
    _event(card, "worker_run_ended", {"run_id": job["id"], "runner": job.get("runner"), "state": state, "run": run,
                                      "device": job.get("host"), "delivery": job.get("delivery"),
                                      **events.fields(state)})
    with connect_closing(board=card.get("board")) as conn:
        kb.add_comment(conn, card["task"], AUTHOR, body)
        wake_for_run(conn, card["task"], job["id"], state)


def record_host(card: dict, link: dict, body: str, payload: dict | None) -> None:
    """A node's outage (*payload*: its ``host.unavailable`` event) or its return (None) on a card: one comment for
    all of the card's runs on that node."""
    if on_hermes(card):
        comment(link["command"], card, body)
        return
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    if payload:
        _event(card, "worker_host_unavailable", payload)
    with connect_closing(board=card.get("board")) as conn:
        kb.add_comment(conn, card["task"], AUTHOR, body)


def card_key(card: dict) -> str:
    return "".join(ch if ch.isalnum() or ch in "-_" else "_" for ch in f"{card.get('board') or '-'}.{card['task']}")


def _parked_path(store, card: dict) -> Path:
    return store.root / "cards" / f"{card_key(card)}.parked"


def park(store, card: dict, open_runs: list[str]) -> None:
    """Record the runs before the card's model schedules itself with ``kanban_schedule``.

    Runs under the card's lock, which every end takes before it wakes the card: an end that lands while the card is
    being parked still finds it parked and wakes it."""
    with store.lock(f"card-{card_key(card)}"):
        mark = _parked_path(store, card)
        mark.parent.mkdir(parents=True, exist_ok=True)
        mark.write_text(" ".join(open_runs) + f"\n{time.time():.3f}\n", encoding="utf-8")


def _last_schedule(view: dict) -> tuple[list[str], str | None]:
    """The runs the card's last ``scheduled`` event names (its ``reason`` and ``goal_park.workers``), and that
    event's identity; ``([], None)`` when it has none."""
    for event in reversed(view.get("events") or []):
        if isinstance(event, dict) and event.get("kind") == "scheduled":
            payload = event.get("payload") if isinstance(event.get("payload"), dict) else {}
            park = payload.get("goal_park") if isinstance(payload.get("goal_park"), dict) else {}
            text = " ".join([str(payload.get("reason") or ""), *map(str, park.get("workers") or [])])
            return list(dict.fromkeys(RUN_ID.findall(text))), f"{event.get('created_at')}:{event.get('run_id')}"
    return [], None


def _still_open(store, run_id: str) -> bool:
    """Hub records win; a foreign run closes only on a successful legacy terminal status."""
    from hermes_cli.worker.hub import ENDED
    from hermes_cli.worker.settings import legacy_omo_task
    ended = ENDED + ("unknown",)
    if run_id.startswith("OG-"):
        members = [job for job in store.all() if job.get("group") == run_id]
        if members:
            return any(job.get("state") not in ended for job in members)
    else:
        job = store.load(run_id)
        if job is not None:
            return job.get("state") not in ended
    legacy = legacy_omo_task(store.worker_root.parent)
    if not run_id.startswith(("OT-", "OG-")) or not legacy:
        return True
    try:
        done = subprocess.run([legacy, "status", "--json", run_id], capture_output=True, text=True,
                              encoding="utf-8", errors="replace", timeout=LEGACY_STATUS_TIMEOUT_S, check=False)
        if done.returncode != 0:
            store.log(f"{run_id} legacy status exited {done.returncode}; keeping open")
            return True
        status = json.loads(done.stdout)
    except (OSError, subprocess.TimeoutExpired, ValueError) as exc:
        store.log(f"{run_id} legacy status unavailable ({type(exc).__name__}); keeping open")
        return True
    return not isinstance(status, dict) or status.get("state") not in ended


def _view_path(store, card: dict) -> Path:
    return store.root / "cards" / f"{card_key(card)}.view.json"


def keep_view(store, card: dict, task: dict) -> None:
    """The card's title, status and body as read at a run's end; the goal drive reads them (``kept_view``) instead
    of asking the board again."""
    path = _view_path(store, card)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({key: task.get(key) for key in ("title", "status", "body")}, ensure_ascii=False),
                    encoding="utf-8")


def kept_view(store, card: dict) -> dict | None:
    try:
        return json.loads(_view_path(store, card).read_text(encoding="utf-8-sig"))
    except (FileNotFoundError, ValueError):
        return None


def wake(store, card: dict, link: dict, run_id: str, judged: str | None = None, *,
         settled: bool = False, drive: bool = False, question: str | None = None) -> str | None:
    """Unblock the card once if it waits on *run_id*, for judging *judged* (the run, or its ``OG-`` batch); the
    unblock form (``judge_wake`` / ``bare``) when it was woken, else None.

    It waits on the run when its last ``scheduled`` event names it: then it is woken after the last run that
    schedule names has ended, including legacy runs whose configured tool confirms their end.
    Else when ``park`` (``lina worker wait``) recorded the run. A card still ``running`` or ``ready`` has not
    scheduled itself yet, so the wake waits, unless the run is *settled* (judged or superseded): the card read the
    end already and will not park on it. The end of a run the card does not wait on, or whose schedule still waits
    on others, leaves it parked: only an open run this hub does not own makes that end wait."""
    with store.lock(f"card-{card_key(card)}"):
        mark = _parked_path(store, card)
        try:
            parked = mark.read_text(encoding="utf-8-sig").splitlines()[0].split()
        except (FileNotFoundError, IndexError):
            parked = []
        try:
            view = json.loads(_kanban(link["command"], card, "show", "--json"))
            status = view["task"]["status"]
        except (ValueError, KeyError, TypeError) as exc:
            raise CardLinkError("kanban show did not return task.status") from exc
        keep_view(store, card, view["task"])
        if question is not None:
            return _wake_question(store, card, link, view, run_id, judged, question, parked)
        from hermes_cli.worker.goal_drive import goal_of, owns_wait, wait_record
        if drive and status == "scheduled" and goal_of(view["task"].get("body")):
            return None  # let the drive choose the next wait before a run-end unblock
        drive_enabled = link.get("goal_drive", True)
        if owns_wait(store, card, view, drive_enabled=drive_enabled):
            return None
        named, schedule = _last_schedule(view)
        if not drive_enabled and wait_record(store, card, view) is not None:
            # Resume ordinary run-end ownership, retaining all linked workers rather than only the last body marker.
            named = list(dict.fromkeys([*named, *(job["id"] for job in store.all() if card in linked_cards(job))]))
        woken = store.root / "cards" / f"{card_key(card)}.woken"
        if run_id in named or (judged in named and judged != run_id):  # a batch member's end is its batch's end
            others = [other for other in named if other != run_id and _still_open(store, other)]
            if others:
                from hermes_cli.worker.settings import legacy_omo_task
                if status == "scheduled" and legacy_omo_task(store.worker_root.parent) and \
                        not all(_on_hub(store, other) for other in others):
                    # A legacy end does not report to this hub; retain its wake. A hub run's own end wakes the card.
                    raise CardWaiting(f"card {card['task']} is waiting for scheduled runs to end", "other runs open")
                return None
            if woken.is_file() and woken.read_text(encoding="utf-8-sig").strip() == schedule:
                return None  # this schedule was woken already, by the end of another run it names
        elif run_id not in parked and status not in ("running", "ready"):
            return None
        if status in ("running", "ready"):  # not parked yet: it may still schedule on this run
            if settled:
                return None
            raise CardWaiting(f"card {card['task']} is {status}; waiting for kanban_schedule", "card running")
        form = None
        if status == "scheduled":
            form = unblock(link["command"], card, judged or run_id)
            if schedule:
                woken.parent.mkdir(parents=True, exist_ok=True)
                woken.write_text(schedule + "\n", encoding="utf-8")
        with contextlib.suppress(FileNotFoundError):
            mark.unlink()
        return form


def _wake_question(store, card: dict, link: dict, view: dict, run_id: str, judged: str | None,
                   question: str, parked: list[str]) -> str | None:
    """A question interrupts only its own scheduled worker wait, without waiting for the other workers to end."""
    if view["task"]["status"] != "scheduled":
        return None
    from hermes_cli.worker.goal_drive import marker, resume_wait, wait_record
    named, _ = _last_schedule(view)
    wait = wait_record(store, card, view)
    reasons = [str(event.get("payload", {}).get("reason") or "") for event in view.get("events") or []
               if event.get("kind") == "scheduled"]
    _, aid = marker(resume_wait(view) or (reasons[-1] if reasons else ""))
    if wait is not None and aid == wait.get("aid"):
        # The body only identifies this schedule; the ledger remains the source of every worker wait.
        named = [item["event"].removeprefix("omo-task:").partition("/")[0]
                 for item in wait.get("items", {}).values() if item["event"].startswith("omo-task:")]
    elif not named:
        named = parked
    if run_id not in named and (judged is None or judged not in named):
        return None
    form = unblock(link["command"], card, judged or run_id, question)
    _parked_path(store, card).unlink(missing_ok=True)
    return form
