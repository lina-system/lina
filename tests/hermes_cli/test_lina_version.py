"""LINA identity: the displayed version, the package version and ``hermes --version`` agree."""

import os
import re
import subprocess
import sys
import tomllib
from pathlib import Path

from hermes_cli.lina_version import LINA_UPSTREAM_COMMIT, LINA_VERSION

REPO_ROOT = Path(__file__).resolve().parents[2]


def _pep440(semver: str) -> str:
    match = re.fullmatch(r"(\d+\.\d+\.\d+)-alpha\.(\d{8})\.(\d+)", semver)
    assert match, f"LINA_VERSION {semver!r} is not 0.0.1-alpha.YYYYMMDD.N"
    return f"{match[1]}a{match[2]}.post{match[3]}"


def test_pyproject_version_is_the_pep440_form_of_lina_version():
    project = tomllib.loads((REPO_ROOT / "pyproject.toml").read_text(encoding="utf-8"))["project"]
    assert project["version"] == _pep440(LINA_VERSION)


def test_version_first_line_names_lina_and_the_upstream_commit(tmp_path):
    env = {**os.environ, "HERMES_HOME": str(tmp_path), "TERMUX_VERSION": ""}
    env.pop("HERMES_DEV", None)
    result = subprocess.run([sys.executable, "-m", "hermes_cli.main", "--version"], capture_output=True,
                            text=True, timeout=60, env=env, cwd=REPO_ROOT, check=False)
    assert result.returncode == 0, result.stderr
    assert result.stdout.splitlines()[0] == f"LINA {LINA_VERSION} (Hermes upstream {LINA_UPSTREAM_COMMIT})"
