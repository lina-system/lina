#!/usr/bin/env python3
"""Entry point of the merge gate statuses; the judging lives in ``hermes_cli/codex_review_gate.py``.

The workflow checks out only these two files (no package install), so the module is loaded from its file and
needs nothing but the standard library. Usage is the module's (``--repo``, ``--pr`` | ``--all`` |
``--merge-group``, ``--dry-run``).
"""

import importlib.util
import sys
from pathlib import Path

MODULE = Path(__file__).resolve().parents[2] / "hermes_cli" / "codex_review_gate.py"

if __name__ == "__main__":
    spec = importlib.util.spec_from_file_location("codex_review_gate", MODULE)
    if spec is None or spec.loader is None:
        sys.exit(f"cannot load {MODULE}")
    gate = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = gate
    spec.loader.exec_module(gate)
    sys.exit(gate.main())
