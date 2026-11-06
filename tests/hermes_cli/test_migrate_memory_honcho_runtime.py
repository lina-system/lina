"""The installed Honcho image supplies dependencies, not the Hermes Python."""

from pathlib import Path
from unittest.mock import patch
import subprocess
from types import SimpleNamespace

import pytest

from hermes_cli.migrate_memory_honcho_stage import upgrade


def test_default_upgrade_uses_installed_image(tmp_path):
    with patch("hermes_cli.migrate_memory_honcho_stage.subprocess.run",
               return_value=subprocess.CompletedProcess([], 0)) as run:
        upgrade("host=127.0.0.1 dbname=copied", "public", "l13_message_split", None, tmp_path)
    argv = run.call_args.args[0]
    assert "honcho-migrate" in argv and "compose" in argv
    assert "/app/.venv/bin/python" in argv
    assert "--rm" in argv
    assert "copied" in run.call_args.kwargs["input"]
    assert "host=db" in run.call_args.kwargs["input"]
    assert "host" not in argv


def test_explicit_interpreter_remains_supported(tmp_path):
    with patch("hermes_cli.migrate_memory_honcho_stage.subprocess.run",
               return_value=subprocess.CompletedProcess([], 0)) as run:
        upgrade("dbname=copied", "public", "l13_message_split", Path("/custom/python"), tmp_path)
    assert run.call_args.args[0][0] == "/custom/python"


@pytest.mark.parametrize("ci,expected", [("true", pytest.fail.Exception), ("", pytest.skip.Exception)])
def test_missing_runtime_fails_ci_and_explains_local_skip(monkeypatch, ci, expected):
    from tests.hermes_cli._migrate_memory_honcho_fixture import honcho_python

    monkeypatch.setenv("CI", ci)
    monkeypatch.setattr("hermes_platform.resolver.locate_command", lambda name: SimpleNamespace(command=[]))
    honcho_python.cache_clear()
    with pytest.raises(expected, match="Honcho migration tests need a running Docker"):
        honcho_python()
