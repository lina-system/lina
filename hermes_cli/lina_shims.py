"""The ``work`` caller shim (lina#37): ``work`` runs ``lina work`` with the same verbs and options, so agents, skills
and cron jobs that call ``work`` keep working. ``lina worker`` has no shim: callers call it by that name (lina#434).

``lina install --shims`` writes it from ``scripts/shims/`` into ``$LINA_HOME/bin/``; putting that folder first on
PATH switches a caller, taking it off switches it back. The shim prints nothing of its own on success (callers parse
the output) and exits with the wrapped command's code. An installed shim always runs in the LINA root it was installed
for (``LINA_HOME``).
"""

from __future__ import annotations

import os
import sys
from dataclasses import dataclass
from pathlib import Path

TOOLS = {"work": "work"}
TEMPLATES = Path(__file__).resolve().parents[1] / "scripts" / "shims"
GENERATED = "Written by `lina install --shims`"  # the header line of every shim this module writes
RETIRED = ("omo-task",)  # shims older installs wrote; ``lina worker`` is called by its own name now (lina#434)


@dataclass(frozen=True)
class Route:
    to: str
    argv: list[str]


def route(tool: str, args: list[str], base: Path | None = None) -> Route:
    """Where one call of *tool* with *args* goes: ``lina work <args>``."""
    if tool not in TOOLS:
        raise ValueError(f"no shim {tool!r} (shims: {', '.join(TOOLS)})")
    return Route("lina", [TOOLS[tool], *args])


def main(argv: list[str] | None = None) -> None:
    tool, *args = sys.argv[1:] if argv is None else argv
    decision = route(tool, args)
    from hermes_cli.main import main as lina_main
    sys.argv = ["lina", *decision.argv]
    lina_main()


def _retire(bin_dir: Path, out) -> None:
    """Remove a shim older installs wrote; leave any other file of that name and say so."""
    for name in RETIRED:
        path = bin_dir / name
        if not path.is_file():
            continue
        try:
            ours = GENERATED in path.read_text(encoding="utf-8-sig")
        except (OSError, UnicodeDecodeError):
            ours = False
        if ours:
            path.unlink()
            out(f"shims: removed the old {name} shim from {bin_dir}; call `lina worker` instead.")
        else:
            out(f"shims: left {path} alone: it was not written by `lina install --shims`.")


def install(base: Path | None = None, *, out=print) -> int:
    """``lina install --shims``: write ``work`` into ``$LINA_HOME/bin/`` for this interpreter, pinned to that LINA
    root, and remove the ``omo-task`` shim an older install wrote there."""
    from hermes_cli.lina_deps import lina_root
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
    _retire(bin_dir, out)
    return 0


if __name__ == "__main__":
    main()
