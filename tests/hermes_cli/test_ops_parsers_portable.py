"""Building the full ``lina`` parser imports nothing POSIX-only (review of lina#38): on Windows ``fcntl`` is absent,
and every ``lina`` command (``lina config``, ``lina gateway run``, help) builds the parser, so a command module may
import its implementation only when that command runs. The child Python blocks ``fcntl`` the way Windows lacks it."""

from __future__ import annotations

import subprocess
import sys
import textwrap
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]

_PROBE = textwrap.dedent("""
    import sys

    class _NoFcntl:
        def find_spec(self, name, path=None, target=None):
            if name == "fcntl":
                raise ModuleNotFoundError("No module named 'fcntl'")
            return None

    sys.modules.pop("fcntl", None)
    sys.meta_path.insert(0, _NoFcntl())
    import argparse
    from hermes_cli.subcommands.install import build_install_parser
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers()
    build_install_parser(sub)
    for name in ("heart", "undo", "learned", "work", "worker"):
        assert name in sub.choices, name
    print("ok")
""")


def test_the_install_parsers_build_without_fcntl(tmp_path):
    env = {"HOME": str(tmp_path), "HERMES_HOME": str(tmp_path / ".lina"), "LINA_HOME": str(tmp_path / ".lina"),
           "PYTHONPATH": str(REPO), "PATH": "/usr/bin:/bin"}
    done = subprocess.run([sys.executable, "-c", _PROBE], cwd=REPO, env=env, capture_output=True, text=True,
                          encoding="utf-8", timeout=120, check=False)
    assert done.returncode == 0 and done.stdout.strip() == "ok", done.stderr[-2000:]
