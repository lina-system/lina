"""Which copy of a runner a run starts (lina#238): LINA's pinned copy once it is installed, else the one on PATH.

The configured ``argv`` names bare commands (``grok``, ``codex-acp``). When ``argv[0]`` is such a bare name, is a
runner pinned in ``pm/lina-deps.json`` and ``$LINA_HOME/bin/runners/<name>`` (``lina_runners.shim_path``) is an
executable file, the run starts that shim, so the copy the daily sync moves is the one that does the work. Before
``lina runners install`` nothing changes. A path (anything with a separator) and a name LINA does not pin are left
as they are. Readiness (``available``), the spawned argv and the ``launched`` event all go through ``command``.
"""

from __future__ import annotations

import os
from pathlib import Path


def command(name: str, root: Path) -> str:
    """LINA's shim for the bare runner name *name* under *root* when it is installed, else *name* unchanged."""
    if os.sep in name or (os.altsep and os.altsep in name):
        return name
    from hermes_cli import lina_runners
    if name not in lina_runners.pins(moved=False):
        return name
    shim = lina_runners.shim_path(root, name)
    return str(shim) if shim.is_file() and os.access(shim, os.X_OK) else name


def argv(base: list[str], root: Path) -> list[str]:
    return [command(base[0], root), *base[1:]] if base else base
