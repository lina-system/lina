"""Commands in a migrated copy's overlay execute from paths with spaces (#737)."""

import re
import subprocess

import pytest

from hermes_cli.migrate_memory_overlay import overlay_text


@pytest.mark.platforms("posix")
def test_overlay_work_command_runs_executable_with_spaces(tmp_path):
    executable = tmp_path / "install with spaces" / "lina"
    executable.parent.mkdir()
    executable.write_text('#!/bin/sh\nprintf "synthetic record: %s %s\\n" "$1" "$2"\n', encoding="utf-8")
    executable.chmod(0o755)
    commands = re.findall(r"`([^`]+)`", overlay_text(str(executable), []))
    command = next(command for command in commands if command.endswith(" work list"))

    done = subprocess.run(["/bin/sh", "-c", command], capture_output=True, text=True, check=False, timeout=10)

    assert done.returncode == 0, done.stderr
    assert done.stdout == "synthetic record: work list\n"
