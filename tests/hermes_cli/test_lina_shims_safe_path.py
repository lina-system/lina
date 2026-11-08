"""LINA's module children ignore a caller checkout's Python modules (lina#428)."""

import os
import subprocess
from pathlib import Path

import pytest

from hermes_cli import lina_shims
from hermes_cli.worker import device, settings
from hermes_cli.worker.runners.base import Task


@pytest.fixture(params=[False, True], ids=["constants", "package"])
def caller(tmp_path, request):
    cwd = tmp_path / "caller"
    cwd.mkdir()
    (cwd / "hermes_constants.py").write_text("raise RuntimeError('decoy constants imported')\n", encoding="utf-8")
    if request.param:
        package = cwd / "hermes_cli"
        package.mkdir()
        (package / "__init__.py").write_text("raise RuntimeError('decoy package imported')\n", encoding="utf-8")
    return cwd


def _env(base: Path) -> dict[str, str]:
    return {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(base.parent),
            "LINA_HOME": str(base), "HERMES_HOME": str(base), "LANG": "C.UTF-8",
            "PYTHONPATH": settings.child_pythonpath()}


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("tool,args", [("omo-task", ["hosts"]), ("work", ["--help"])])
def test_installed_shim_routes_from_a_decoy_checkout(tmp_path, caller, tool, args):
    base = tmp_path / "lina"
    assert lina_shims.install(base, out=lambda line: None) == 0

    done = subprocess.run([str(base / "bin" / tool), *args], cwd=caller, env=_env(base),
                          capture_output=True, text=True, encoding="utf-8", timeout=120, check=False)

    assert done.returncode == 0, done.stderr
    if tool == "work":
        assert "--help" in done.stdout and "work" in done.stdout
    else:
        assert "queue: 0" in done.stdout


def test_worker_run_argv_reaches_lina_from_a_decoy_checkout(tmp_path, caller):
    base = tmp_path / "lina"
    task = Task("OT-SAFE1", base / "worker" / "tasks" / "OT-SAFE1", {})
    argv = device.run_argv(task, 1)

    done = subprocess.run([*argv, "--help"], cwd=caller, env=_env(base),
                          capture_output=True, text=True, encoding="utf-8", timeout=120, check=False)

    assert done.returncode == 0, done.stderr
    assert "_run" in done.stdout
    assert argv[1:4] == ["-P", "-m", "hermes_cli.main"]
