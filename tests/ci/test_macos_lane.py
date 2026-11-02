"""The ``macos`` lane of scripts/ci/classify_changes.py (#86).

The macOS job ran only after a merge, so a pull request fixing a macOS failure (#85) merged
without its macOS run and dev stayed red. A pull request now runs it when it changes a
macOS-marked test or a module one imports, or carries the dev-red-fix label.
"""

from __future__ import annotations

import importlib.util
import io
import re
import sys
from pathlib import Path

import pytest

_REPO = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location("classify_changes", _REPO / "scripts" / "ci" / "classify_changes.py")
_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)


def _tree(tmp_path: Path) -> Path:
    tests = tmp_path / "tests" / "pkg"
    tests.mkdir(parents=True)
    (tests / "test_marked.py").write_text(
        "import pytest\nimport alpha.beta\n\n\n@pytest.mark.platforms(\"linux\", \"macos\")\n"
        "def test_x():\n    from gamma import delta\n    from epsilon.zeta import eta\n",
        encoding="utf-8")
    (tests / "test_linux_only.py").write_text(
        "import pytest\nimport unrelated\n\n\n@pytest.mark.platforms(\"linux\")\ndef test_y():\n    pass\n",
        encoding="utf-8")
    return tmp_path


@pytest.mark.parametrize("changed", [
    "tests/pkg/test_marked.py",   # the marked test itself
    "alpha/beta.py",              # a module it imports at the top
    "gamma/delta.py",             # a submodule named by a function-level from-import
    "gamma/__init__.py",
    "epsilon/zeta.py",
])
def test_a_marked_test_or_a_module_it_imports_runs_the_lane(tmp_path, changed):
    assert _mod.macos_lane([changed], root=_tree(tmp_path))


@pytest.mark.parametrize("changed", ["unrelated.py", "tests/pkg/test_linux_only.py", "README.md", "eta.py"])
def test_other_changes_skip_the_lane(tmp_path, changed):
    assert not _mod.macos_lane([changed], root=_tree(tmp_path))


def test_the_red_fix_label_and_fail_open_run_the_lane(tmp_path):
    root = _tree(tmp_path)
    assert _mod.macos_lane(["README.md"], red_fix=True, root=root)
    assert _mod.macos_lane([], root=root)
    assert _mod.macos_lane([".github/workflows/ci.yml"], root=root)


def test_the_change_that_left_dev_red_would_have_run_the_lane():
    """#85 changed hermes_state_reconcile.py, which a macOS-marked test imports."""
    assert _mod.macos_lane(["hermes_state_reconcile.py"])
    assert not _mod.macos_lane(["README.md"])


def test_main_emits_the_lane_from_the_label(monkeypatch, capsys):
    monkeypatch.setattr(sys, "stdin", io.StringIO("README.md\n"))
    monkeypatch.delenv("GITHUB_OUTPUT", raising=False)
    monkeypatch.setattr(_mod, "pull_request_labels", list)
    assert _mod.main() == 0
    assert "macos=false" in capsys.readouterr().out.splitlines()

    monkeypatch.setattr(sys, "stdin", io.StringIO("README.md\n"))
    monkeypatch.setattr(_mod, "pull_request_labels", lambda: [_mod.DEV_RED_FIX_LABEL])
    assert _mod.main() == 0
    assert "macos=true" in capsys.readouterr().out.splitlines()


def test_ci_gates_the_macos_job_on_an_output_detect_declares():
    """An undeclared ``needs.detect.outputs.macos`` reads as "" and skips the job on every PR."""
    yaml = pytest.importorskip("hermes_yaml")
    ci = yaml.safe_load((_REPO / ".github/workflows/ci.yml").read_text(encoding="utf-8-sig"))
    action = yaml.safe_load((_REPO / ".github/actions/detect-changes/action.yml").read_text(encoding="utf-8-sig"))
    gate = ci["jobs"]["macos"]["if"]
    assert "detect" in ci["jobs"]["macos"]["needs"]
    referenced = set(re.findall(r"needs\.detect\.outputs\.(\w+)", gate))
    assert referenced == {"macos"}
    assert "macos" in ci["jobs"]["detect"]["outputs"] and "macos" in action["outputs"]
