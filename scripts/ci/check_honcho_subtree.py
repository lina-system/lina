#!/usr/bin/env python3
"""Check the Honcho subtree in ``services/honcho`` against the upstream commit it came from.

``services/honcho`` is a squashed ``git subtree`` of plastic-labs/honcho (docs/honcho.md). The checks are
shared with the LCM-X subtree (``scripts/ci/subtree_check.py``); Honcho is AGPL-3.0, so
``services/honcho/README.md`` must also start with the LINA modification notice naming the ledger's
upstream commit.

    python scripts/ci/check_honcho_subtree.py
    python scripts/ci/check_honcho_subtree.py --mirror origin/honcho-upstream

Exit codes: 0 = clean, 1 = at least one finding, 2 = git or usage error.
"""

from __future__ import annotations

import sys
from pathlib import Path

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from scripts.ci.subtree_check import Subtree, main as _main

HONCHO = Subtree(name="check_honcho_subtree", prefix="services/honcho", ledger="services/honcho-intake.md",
                 mirror="honcho-upstream", modification_notice=True)


def main(argv: list[str] | None = None) -> int:
    return _main(HONCHO, __doc__.split("\n\n")[0], argv)


if __name__ == "__main__":
    raise SystemExit(main())
