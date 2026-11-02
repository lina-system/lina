"""scripts/vendored_subtrees.py: the one list of vendored subtrees the repo-wide guards skip (lina#15)."""

from __future__ import annotations

import tomllib
from pathlib import Path, PureWindowsPath

from scripts.code_health.config import PY_EXCLUDE
from scripts.vendored_subtrees import VENDORED_SUBTREES, is_vendored

REPO_ROOT = Path(__file__).resolve().parents[2]


def test_ruff_excludes_exactly_the_vendored_subtrees():
    """ruff.toml cannot import the list, so the two must agree."""
    config = tomllib.loads((REPO_ROOT / "ruff.toml").read_text(encoding="utf-8-sig"))
    assert sorted(config["extend-exclude"]) == sorted(VENDORED_SUBTREES)


def test_code_health_excludes_every_vendored_subtree():
    """scripts/code_health/config.py spells the list out because fixture repos copy only that package."""
    assert {f"{subtree}/*" for subtree in VENDORED_SUBTREES} <= set(PY_EXCLUDE)


def test_is_vendored_matches_whole_path_components_only():
    assert is_vendored("plugins/context_engine/lcm-x/engine.py")
    assert is_vendored("./services/honcho/src/main.py")
    assert is_vendored(PureWindowsPath("plugins\\context_engine\\lcm-x\\tests\\conftest.py"))
    assert is_vendored("services/honcho")
    assert not is_vendored("plugins/context_engine/lcm-x-intake.md")
    assert not is_vendored("plugins/context_engine/__init__.py")
    assert not is_vendored("services/honcho-intake.md")
