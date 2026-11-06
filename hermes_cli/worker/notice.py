"""The one short notice a run's end sends to the agent that handed it out: state, where the result is, what to do.

It never pastes ``report.md`` or the final answer. It goes to the agent's chat through ``worker.notice.command``
(argv, ``{agent}`` filled in, the body on stdin) and is always kept in ``hub/notices/``; a run started from a work
card also gets it as a comment on that card (``kanban_link``).
"""

from __future__ import annotations

import re
import shlex
import subprocess
import time
from pathlib import Path

from hermes_cli.worker.report import NEXT_NOTES

STATE_WORDS = {"queued": "queued (no free device)", "running": "running", "done": "done",
               "done_empty": "nothing delivered", "review": "needs review", "stalled": "no answer (stalled)",
               "failed": "failed", "cancelled": "cancelled", "unknown": "device unreachable", "new": "starting",
               "human": "waiting for an answer"}
DELIVERY_WORDS = {"pending": "not fetched yet", "ok": "fetched, hashes match", "mismatch": "fetched, hashes differ",
                  "missing": "no manifest"}
ENVIRONMENT = re.compile(r"\b(40[1378]|429|5\d\d)\b|proxy|network|timed? ?out|OOM|login|auth|credential|quota|"
                         r"rate.?limit|not authenticated|transport_gone|host_unavailable", re.IGNORECASE)


def _head(job: dict, run: str) -> str:
    what = f"to-do {job['todo']}" if job.get("todo") else f"topic {job.get('topic')}"
    access = job.get("run_access", {}).get(str(run), job.get("access", "workspace-write"))
    return (f"[lina worker · automatic] {job['bot']} · {what} · run {job['id']} ({job.get('runner')} on "
            f"{job.get('host') or '-'}, run {run}, access {access}): ")


def _result_line(job: dict, out_dir: Path) -> str:
    return f"- Result: {out_dir}/ (report.md, out/" + (", git.txt" if job.get("repo") else "") + ")\n"


def _delivery(job: dict, check: dict) -> str:
    text = ""
    if job.get("delivery", "pending") != "ok":
        text += (f"- Delivery check: {DELIVERY_WORDS.get(job.get('delivery', 'pending'))} → fetch again with "
                 f"`lina worker result {job['id']}`.\n")
    if check.get("inputs_unchanged") is False:
        text += "- The read-only input copies changed: do not apply the result; tell the owner.\n"
    return text


def _accept(job: dict) -> str:
    return f"`lina worker accept {job['id']} --note \"<what you checked>\"`"


def _ended(job: dict, kind: str, out_dir: Path, check: dict, notes: int | None) -> str:
    tid = job["id"]
    if kind == "done":
        return ("done\n" + _result_line(job, out_dir) + _delivery(job, check) + "To do:\n"
                f"1. Open report.md and out/ and hold them against the done conditions. If they meet them, "
                f"{_accept(job)}; if not, reject and then `lina worker send {tid} \"…\"`.\n"
                f"2. Copy the '{NEXT_NOTES}' section of report.md into the notes of that field.\n")
    gap = "is missing" if notes is None else "is empty"
    return (f"needs review — the '{NEXT_NOTES}' section of report.md {gap}\n" + _result_line(job, out_dir)
            + _delivery(job, check) + "To do:\n"
            "1. Hold report.md and out/ against the done conditions.\n"
            f"2. To have it filled: `lina worker send {tid} \"fill the {NEXT_NOTES} section of report.md\"`.\n"
            f"3. If it meets them, {_accept(job)}.\n")


def _failed(job: dict, out_dir: Path, err_tail: str) -> str:
    final = (out_dir / "final.md").read_text(encoding="utf-8-sig", errors="replace") if (out_dir / "final.md").is_file() \
        else ""
    environment = bool(ENVIRONMENT.search(err_tail + final))
    tid = job["id"]
    return ("failed — " + ("looks like a problem of the device's environment" if environment
                           else "looks like a problem of the task") + "\n"
            f"- End of the error output: {err_tail.strip()[-160:] or '-'}\n"
            f"- Details: `lina worker status {tid}`, `lina worker receipts {tid}`\nTo do:\n"
            f"1. Fix the cause (environment problems go to the device's owner), then `lina worker send {tid} "
            "\"go on\"`.\n"
            f"2. A task problem: `lina worker send {tid} \"<what to add>\"`. Submit a new task only if the work is lost.\n")


def _done_empty(job: dict, out_dir: Path, **_) -> str:
    tid = job["id"]
    return ("nothing delivered — neither report.md nor files under out/\n" + _result_line(job, out_dir)
            + f"To do:\n1. `lina worker status {tid}` for the reason.\n"
            f"2. `lina worker reject {tid} --why \"no result\"`, then `lina worker send {tid} \"write the result "
            "to report.md and out/ and finish\"`.\n")


def _stalled(job: dict, out_dir: Path, *, quiet_s: float, **_) -> str:
    tid = job["id"]
    return (f"no answer — nothing recorded for over {int(quiet_s) // 60} min; the session is left open\n"
            + _result_line(job, out_dir) + f"To do:\n1. `lina worker status {tid}`. If everything is there, "
            "check it and accept.\n"
            f"2. More to do: `lina worker send {tid} \"…\"`; to drop it: `lina worker cancel {tid} --why …`.\n")


def _human(job: dict, out_dir: Path, **_) -> str:
    tid = job["id"]
    return (f"waiting for an answer — the agent asked a question\nTo do: read it with `lina worker status {tid}` "
            f"and answer with `lina worker send {tid} \"<answer>\"`; to stop: `lina worker cancel {tid} --why …`.\n")


def _cancelled(job: dict, out_dir: Path, **_) -> str:
    return f"cancelled — {job.get('cancel_why') or '-'}\n- Results so far: {out_dir}/\n"


def _unknown(job: dict, out_dir: Path, *, unknown_after_s: int, **_) -> str:
    return (f"device unreachable — no contact with {job.get('host')} for over {unknown_after_s // 60} min; "
            f"the task was not moved to another device.\nTo do: when it is back, `lina worker status {job['id']}`.\n")


_BODIES = {
    "done": lambda job, out_dir, check, notes, **_: _ended(job, "done", out_dir, check, notes),
    "review": lambda job, out_dir, check, notes, **_: _ended(job, "review", out_dir, check, notes),
    "done_empty": _done_empty, "stalled": _stalled, "human": _human, "cancelled": _cancelled, "unknown": _unknown,
}


def body(job: dict, kind: str, run: str, out_dir: Path, check: dict, *, notes: int | None = None,
         quiet_s: float = 0, err_tail: str = "", unknown_after_s: int = 600) -> str:
    """The notice for the end *kind* of run *run*; any other end reads as failed."""
    write = _BODIES.get(kind)
    text = (write(job, out_dir, check=check, notes=notes, quiet_s=quiet_s, unknown_after_s=unknown_after_s)
            if write else _failed(job, out_dir, err_tail))
    return _head(job, run) + text


def deliver(job: dict, text: str, tag: str, notices_dir: Path, command: list[str] | None) -> str:
    """Keep the notice under ``notices_dir`` and hand it to the agent's chat; returns how it went."""
    notices_dir.mkdir(parents=True, exist_ok=True)
    (notices_dir / f"{job['id']}-{tag}-{int(time.time())}.md").write_text(text, encoding="utf-8")
    if not command:
        return "kept in hub/notices (no worker.notice.command configured)"
    argv = [str(each).format(agent=job["bot"], id=job["id"]) for each in command]
    if job.get("dry_notify"):
        return "DRY " + shlex.join(argv)
    try:
        subprocess.run(argv, input=text.encode("utf-8"), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                       timeout=60, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        return f"notice command failed: {exc}"
    return f"{argv[0]} -> {job['bot']}"
