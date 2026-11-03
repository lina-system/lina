"""The built wheel carries every non-Python file of ``hermes_cli/work`` (lina#36): the SQLite backend reads
``schema_sqlite.sql`` beside its module on every connection, so a wheel without it fails on first use."""

import os
import subprocess
import sys
import zipfile
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
WORK = PROJECT_ROOT / "hermes_cli" / "work"


def _build_wheel(tmp_path: Path) -> Path:
    scratch = tmp_path / "scratch"
    scratch.mkdir()
    extra_cfg = tmp_path / "dist-extra.cfg"
    extra_cfg.write_text(f"[build]\nbuild_base = {scratch / 'build'}\n\n[egg_info]\negg_base = {scratch}\n",
                         encoding="utf-8")
    env = dict(os.environ, HERMES_NIX_BUILD="1", DIST_EXTRA_CONFIG=str(extra_cfg))
    out = tmp_path / "dist"
    result = subprocess.run(
        [sys.executable, "-c", f"from setuptools.build_meta import build_wheel; print(build_wheel(r'{out}'))"],
        cwd=PROJECT_ROOT, env=env, text=True, encoding="utf-8", capture_output=True, timeout=600, check=False)
    assert result.returncode == 0, result.stderr[-2000:]
    return out / result.stdout.strip().splitlines()[-1]


def test_wheel_ships_the_work_package_resources(tmp_path):
    resources = {p.relative_to(PROJECT_ROOT).as_posix() for p in WORK.rglob("*")
                 if p.is_file() and p.suffix not in (".py", ".pyc") and "__pycache__" not in p.parts}
    assert "hermes_cli/work/schema_sqlite.sql" in resources
    with zipfile.ZipFile(_build_wheel(tmp_path)) as wheel:
        shipped = set(wheel.namelist())
    assert resources <= shipped, sorted(resources - shipped)
