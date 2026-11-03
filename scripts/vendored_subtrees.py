"""The vendored upstream subtrees, in one place.

Each is a squashed ``git subtree`` held to its upstream's own lint and tests (docs/honcho.md, docs/lcm-x.md),
so LINA's repo-wide guards that walk source files skip them. A guard reads this list instead of its own copy.
"""

from __future__ import annotations

from pathlib import PurePath

VENDORED_SUBTREES = ("services/honcho", "plugins/context_engine/lcm-x")


def is_vendored(rel_path: str | PurePath) -> bool:
    """Whether a repository-relative path (either separator, optional leading ``./``) is in a vendored subtree."""
    rel = str(rel_path).replace("\\", "/").removeprefix("./")
    return any(rel == prefix or rel.startswith(prefix + "/") for prefix in VENDORED_SUBTREES)
