"""The migrated copy's own work-record instruction (lina#719).

The copied persona, notes, skills and summary rows say where work status lived on the previous machine (its
kanban board, the ``hermes kanban`` wrapper, ``session-queue``, ssh to its hosts). They are copied byte for byte
and many of their other lines stay true, so nothing is rewritten: one generated file in the copy's home, loaded
into the system prompt right after SOUL.md (``agent.prompt_builder.load_copy_overlay``), names the copy's own
``lina work`` and supersedes those status lines. It is the migration's own output and is rewritten on every run.
"""

from __future__ import annotations

import os
import re
import shlex
from pathlib import Path
from typing import Sequence

from agent.prompt_builder import COPY_OVERLAY_NAME
from hermes_cli.migrate_memory_copy import _safe

_HOST = re.compile(r"[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?")


def old_hosts(values: Sequence[str]) -> list[str]:
    """``--old-host`` values as host names, in order and once each; anything else is refused."""
    hosts = list(dict.fromkeys(value.strip() for value in values))
    if bad := [host for host in hosts if not _HOST.fullmatch(host)]:
        raise ValueError(f"--old-host must be a host name: {', '.join(bad)}")
    return hosts


def _own_lina() -> str | None:
    """Absolute path of this install's ``lina`` (``hermes`` is the same entry point), or None."""
    from tools.environments.local import install_command_dir

    bin_dir = install_command_dir()
    for name in ("lina", "hermes"):
        if bin_dir and os.path.isfile(command := os.path.join(bin_dir, name)):
            return command
    return None


def overlay_text(lina: str, hosts: Sequence[str]) -> str:
    lina = shlex.quote(lina)
    listed = ", ".join(f"`{host}`" for host in hosts)
    machine = f"the previous machine ({listed})" if hosts else "the previous machine"
    others = f"the hosts {listed}" if hosts else "its hosts"
    return f"""## Work status on this copy

This agent was copied to this LINA home by `lina migrate memory`. The persona, notes, skills and summaries above
and in memory were copied unchanged; where they say work status comes from a kanban board, from `hermes kanban`
or `session-queue`, or from a command run on another host, they describe {machine}. This section replaces those
status lines.

- Your own work record is `lina work`: run `{lina} work list` for the goals and to-dos and their state, and
  `{lina} work show <id>` for one of them. Answer what is in progress, planned, waiting or done from its output.
- The old kanban board, `hermes kanban` (also as `hermes -p <profile> kanban ...`), `session-queue` and
  {others} belong to the previous machine. Do not use them, and do not ssh there, for work status.
- If `{lina} work` fails, say what it printed; do not fall back to the old board.
"""


def write_overlay(destination: Path, hosts: Sequence[str]) -> dict:
    lina = _own_lina()
    target = _safe(destination / COPY_OVERLAY_NAME)
    staging = target.with_name(f".{COPY_OVERLAY_NAME}.tmp")
    staging.write_text(overlay_text(lina or "lina", hosts), encoding="utf-8")
    staging.replace(target)
    return {"file": COPY_OVERLAY_NAME, "lina": lina, "old_hosts": list(hosts)}
