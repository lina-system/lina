"""The caller shims for the switch to LINA (lina#37): ``omo-task`` runs ``lina worker`` and ``work`` runs ``lina work``
with the same verbs and options, so agents, skills and cron jobs keep calling the old names meanwhile.

``lina install --shims`` writes them from ``scripts/shims/`` into ``$LINA_HOME/bin/``; putting that folder first on
PATH switches a caller, taking it off switches it back. A shim prints nothing of its own on success (callers parse the
output) and exits with the wrapped command's code.

Who owns a task ID: ``lina worker``'s hub record (``worker/hub/jobs/<ID>.json``) is the registry of the IDs it started.
An ID it does not hold was started by the old tool, and goes to the old ``omo-task`` kept beside it when
``worker.legacy_omo_task`` names it. One call goes to one tool, never both.
"""

from __future__ import annotations

import os
import sys
from dataclasses import dataclass
from pathlib import Path

TOOLS = {"omo-task": "worker", "work": "work"}
ID_VERBS = frozenset({"status", "send", "cancel", "result", "accept", "reject", "fork", "receipts", "unschedule"})
NO_FORK = ("omo-task fork: lina worker has no fork. Submit a new brief instead (`omo-task submit --brief <file> "
           "--input <the task's out/>`), or `omo-task send <ID> <message>` for the next run in the same session.")
TEMPLATES = Path(__file__).resolve().parents[1] / "scripts" / "shims"


@dataclass(frozen=True)
class Route:
    to: str  # "lina", "legacy" or "refuse"
    argv: list[str]
    message: str = ""


def _task_id(args: list[str]) -> str | None:
    if not args or args[0] not in ID_VERBS:
        return None
    return next((arg for arg in args[1:] if not arg.startswith("-")), None)


def legacy_omo_task(base: Path | None = None) -> str | None:
    from hermes_cli.worker import settings
    value = settings.section(base).get("legacy_omo_task")
    return str(Path(value).expanduser()) if isinstance(value, str) and value.strip() else None


def owned_by_lina(task_id: str, base: Path | None = None) -> bool:
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub_store import Store
    return Store(settings.root(base)).path(task_id).is_file()


def route(tool: str, args: list[str], base: Path | None = None) -> Route:
    """Where one call of *tool* with *args* goes: ``lina <worker|work> <args>`` or the old ``omo-task``."""
    if tool not in TOOLS:
        raise ValueError(f"no shim {tool!r} (shims: {', '.join(TOOLS)})")
    task_id = _task_id(args) if tool == "omo-task" else None
    if task_id and not owned_by_lina(task_id, base) and (legacy := legacy_omo_task(base)):
        return Route("legacy", [legacy, *args])
    if tool == "omo-task" and args[:1] == ["fork"]:
        return Route("refuse", [], NO_FORK)
    return Route("lina", [TOOLS[tool], *args])


def main(argv: list[str] | None = None) -> None:
    tool, *args = sys.argv[1:] if argv is None else argv
    decision = route(tool, args)
    if decision.to == "refuse":
        print(decision.message, file=sys.stderr)
        sys.exit(2)
    if decision.to == "legacy":
        os.execv(decision.argv[0], decision.argv)
    from hermes_cli.main import main as lina_main
    sys.argv = ["lina", *decision.argv]
    lina_main()


def install(base: Path | None = None, *, out=print) -> int:
    """``lina install --shims``: write ``omo-task`` and ``work`` into ``$LINA_HOME/bin/`` for this interpreter."""
    from hermes_cli.lina_ocx import lina_root
    from hermes_cli.worker.settings import child_pythonpath
    root = base or lina_root()
    if not TEMPLATES.is_dir():
        out(f"lina install --shims: no shim templates at {TEMPLATES} (install LINA from its repository)")
        return 1
    bin_dir = root / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    values = {"@PYTHON@": sys.executable, "@PYTHONPATH@": child_pythonpath(), "@LINA_HOME@": str(root)}
    for tool in TOOLS:
        text = (TEMPLATES / tool).read_text(encoding="utf-8-sig")
        for key, value in values.items():
            text = text.replace(key, value)
        target = bin_dir / tool
        tmp = target.with_name(f".{tool}.tmp")
        tmp.write_text(text, encoding="utf-8")
        tmp.chmod(0o755)
        os.replace(tmp, target)
    out(f"shims: {', '.join(TOOLS)} in {bin_dir}. Put it first on PATH to switch a caller (docs/switch-callers.md).")
    return 0


if __name__ == "__main__":
    main()
