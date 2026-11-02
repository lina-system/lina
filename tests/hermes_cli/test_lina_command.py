"""`lina` is the command and `hermes` another name for the same entry point (lina#29).

Both names must take the same arguments, print the same output and exit with the same code,
and every process-identity matcher that recognizes a running `hermes` must recognize `lina`.
"""
import subprocess
import sys
from pathlib import Path

import pytest

from gateway.status import looks_like_gateway_command_line
from hermes_cli.profiles import _is_hermes_argv
from hermes_cli.update_cmd_windows import _hermes_holder_subcommand
from hermes_state_holders import _looks_like_hermes

ROOT = Path(__file__).resolve().parents[2]


def _run(name: str, args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run([sys.executable, str(ROOT / name), *args], cwd=ROOT, capture_output=True,
                          text=True, encoding="utf-8", timeout=120, check=False)


@pytest.mark.parametrize("args", [["--version"], ["--help"], ["no-such-command"]])
def test_lina_and_hermes_answer_alike(args):
    lina, hermes = _run("lina", args), _run("hermes", args)
    assert (lina.returncode, lina.stdout, lina.stderr) == (hermes.returncode, hermes.stdout, hermes.stderr)


def test_unknown_command_fails_with_the_usage_exit_code():
    assert _run("lina", ["no-such-command"]).returncode == 2


@pytest.mark.parametrize("program", ["lina", "/opt/lina/bin/lina", "lina.exe"])
def test_identity_matchers_recognize_lina(program):
    assert looks_like_gateway_command_line(f'"{program}" gateway run')
    assert not looks_like_gateway_command_line(f'"{program}" gateway status')
    assert _hermes_holder_subcommand(f'"{program}" --profile work serve') == "serve"
    assert _looks_like_hermes([program, "chat"])
    assert _is_hermes_argv([program, "chat"])


def test_python_running_the_lina_script_is_lina():
    assert _is_hermes_argv(["python3", "/home/u/.local/bin/lina", "gateway", "run"])
    assert not _is_hermes_argv(["python3", "/home/u/linaria.py"])
