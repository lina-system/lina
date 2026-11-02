"""Optional terminal providers disappear cleanly with their implementation files."""

import os
from pathlib import Path
import shutil
import subprocess
import sys

import pytest


def test_local_execution_without_optional_backend_modules(tmp_path):
    root = Path(__file__).resolve().parents[2]
    tools = tmp_path / "tools"
    shutil.copytree(root / "tools", tools, ignore=shutil.ignore_patterns("__pycache__"))
    for name in ("docker", "docker_egress", "docker_ownership", "modal", "managed_modal",
                 "daytona", "vercel_sandbox"):
        (tools / "environments" / f"{name}.py").unlink()
    env = {**os.environ, "PYTHONPATH": os.pathsep.join((str(tmp_path), str(root)))}
    code = """
from tools.terminal_tool_backends import _create_environment
from tools.environments.discovery import discover_environment_providers
discover_environment_providers()
env = _create_environment('local', image='', cwd='.', timeout=10,
                          local_config={'persistent': False})
try:
    result = env.execute('printf kept-local')
    assert result['returncode'] == 0, result
    assert 'kept-local' in result['output'], result
finally:
    env.cleanup()
from hermes_cli import doctor_tools, setup_terminal, sandbox_image_switch
from hermes_cli.web_routers import tools
"""
    result = subprocess.run([sys.executable, "-c", code], cwd=tmp_path, env=env,
                            capture_output=True, text=True, timeout=60, check=False)
    assert result.returncode == 0, result.stdout + result.stderr


@pytest.mark.parametrize("backend", ["docker", "modal", "daytona", "vercel_sandbox"])
def test_present_backend_registers_its_factory(backend):
    from tools.terminal_tool_config import _get_plugin_env_provider

    provider = _get_plugin_env_provider(backend)
    assert provider is not None
    assert provider.name == backend
    assert callable(provider.create_environment)
