"""The part of ``helper/macos``'s ``make test`` that runs without a Mac (lina#45).

The unlock package's C logic tests and its Python tests, and the installer/signing/bundling tests, are the
LINA Helper repository's own standard-library suites; they run here unchanged so the Linux CI lane covers them.
Swift and native macOS checks run in the macOS workflow (``.github/workflows/helper-macos.yml``).
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

import pytest

HELPER = Path(__file__).resolve().parents[2] / "helper" / "macos"


def _run(argv, cwd, **env):
    p = subprocess.run(argv, cwd=cwd, capture_output=True, text=True, check=False, timeout=300,
                       env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1", **env})
    assert p.returncode == 0, p.stdout[-4000:] + p.stderr[-4000:]
    return p.stdout + p.stderr


@pytest.mark.platforms("posix")
def test_unlock_package_tests(tmp_path):
    build = os.path.relpath(tmp_path, HELPER / "unlock")  # the Makefile runs ./$(BUILD)/<test>
    out = _run(["make", "-C", str(HELPER / "unlock"), "test", f"BUILD={build}"], HELPER)
    assert "PASS" in out and "OK" in out


def test_installer_signing_and_bundle_tests():
    out = _run([sys.executable, "-m", "unittest", "discover", "-s", "tools/tests", "-v"], HELPER)
    assert "OK" in out
