"""The caller shims for the switch to LINA (lina#37): ``omo-task`` runs ``lina worker`` and ``work`` runs ``lina work``
with the same verbs and options, so agents, skills and cron jobs keep calling the old names meanwhile.

``lina install --shims`` writes them from ``scripts/shims/`` into ``$LINA_HOME/bin/``; putting that folder first on
PATH switches a caller, taking it off switches it back. A shim prints nothing of its own on success (callers parse the
output) and exits with the wrapped command's code.

Who owns an ID: ``lina worker`` reserves every minted ID in ``worker/hub/ids/<ID>``; older IDs are held in job,
schedule or batch receipt records.
An ID it does not hold was started by the old tool, and goes to the old ``omo-task`` kept beside it when
``worker.legacy_omo_task`` names it. One call goes to one tool, never both, except ``list``: ``lina worker``'s tasks,
then the old tool's ``list``.

An installed shim always runs in the LINA root it was installed for (``LINA_HOME``). A caller's ``HERMES_HOME``
outside that root is Hermes Agent's profile home: it is taken out of the environment (LINA would drop it) and kept
as ``LINA_CALLER_HERMES_HOME``, so ``lina worker`` can name the caller's profile as a run's owner.
"""

from __future__ import annotations

import os
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

TOOLS = {"omo-task": "worker", "work": "work"}
ID_VERBS = frozenset({"status", "send", "cancel", "result", "accept", "reject", "fork", "receipts", "unschedule",
                      "attach", "card"})
NO_FORK = ("omo-task fork: lina worker has no fork. Submit a new brief instead (`omo-task submit --brief <file> "
           "--input <the task's out/>`), or `omo-task send <ID> <message>` for the next run in the same session.")
LEGACY_LIST_TIMEOUT_S = 120
TEMPLATES = Path(__file__).resolve().parents[1] / "scripts" / "shims"


@dataclass(frozen=True)
class Route:
    to: str  # "lina", "legacy" or "refuse"
    argv: list[str]
    message: str = ""
    then: list[str] = field(default_factory=list)  # the old tool's argv run after lina's (``list``)


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
    store = Store(settings.root(base))
    return ((store.root / "ids" / task_id).is_file() or store.path(task_id).is_file()
            or (store.schedules / f"{task_id}.json").is_file()
            or any(record.get("group") == task_id for record in store.receipts_of(task_id)))


def route(tool: str, args: list[str], base: Path | None = None) -> Route:
    """Where one call of *tool* with *args* goes: ``lina <worker|work> <args>`` or the old ``omo-task``."""
    if tool not in TOOLS:
        raise ValueError(f"no shim {tool!r} (shims: {', '.join(TOOLS)})")
    task_id = _task_id(args) if tool == "omo-task" else None
    if task_id and not owned_by_lina(task_id, base) and (legacy := legacy_omo_task(base)):
        return Route("legacy", [legacy, *args])
    if tool == "omo-task" and args[:1] == ["fork"]:
        return Route("refuse", [], NO_FORK)
    if tool == "omo-task" and args[:1] == ["list"] and (legacy := legacy_omo_task(base)):
        return Route("lina", [TOOLS[tool], *args], then=[legacy, *args])
    return Route("lina", [TOOLS[tool], *args])


def keep_caller_home(environ=os.environ) -> None:
    """Move a ``HERMES_HOME`` outside the LINA root (Hermes Agent's) to ``LINA_CALLER_HERMES_HOME``."""
    from hermes_cli.worker import settings
    raw = environ.get("HERMES_HOME", "").strip()
    if not raw:
        return
    home, root = Path(raw).expanduser().resolve(), settings.lina_home().resolve()
    if home != root and root not in home.parents:
        environ[settings.CALLER_HERMES_HOME] = environ.pop("HERMES_HOME")


def main(argv: list[str] | None = None) -> None:
    tool, *args = sys.argv[1:] if argv is None else argv
    keep_caller_home()
    decision = route(tool, args)
    if decision.to == "refuse":
        print(decision.message, file=sys.stderr)
        sys.exit(2)
    if decision.to == "legacy":
        os.execv(decision.argv[0], decision.argv)
    from hermes_cli.main import main as lina_main
    sys.argv = ["lina", *decision.argv]
    if not decision.then:
        lina_main()
        return
    try:
        lina_main()
        code = 0
    except SystemExit as ended:
        code = ended.code if isinstance(ended.code, int) else (0 if ended.code is None else 1)
    sys.stdout.flush()
    try:
        legacy = subprocess.run(decision.then, check=False, timeout=LEGACY_LIST_TIMEOUT_S).returncode
    except subprocess.TimeoutExpired:
        print(f"omo-task list: the old tool did not answer in {LEGACY_LIST_TIMEOUT_S} s", file=sys.stderr)
        legacy = 1
    sys.exit(code or legacy)


def install(base: Path | None = None, *, out=print) -> int:
    """``lina install --shims``: write ``omo-task`` and ``work`` into ``$LINA_HOME/bin/`` for this interpreter, each
    pinned to that LINA root."""
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
