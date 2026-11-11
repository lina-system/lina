"""File-node end marks must be sampled after process liveness (lina#415)."""

import os
import shlex
import shutil
import subprocess
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest

from hermes_cli.worker import file_node_bash


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("completed", [True, False], ids=["completed", "vanished"])
def test_status_observes_a_runner_ending_during_the_probe(tmp_path, completed):
    task = tmp_path / "task"
    task.mkdir()
    release, reaped = tmp_path / "release", tmp_path / "reaped"
    os.mkfifo(release)
    os.mkfifo(reaped)
    tools = tmp_path / "bin"
    tools.mkdir()
    cat = tools / "cat"
    real_cat = shlex.quote(shutil.which("cat"))
    cat.write_text(
        '#!/bin/sh\n'
        'case "$1" in */pid.2)\n'
        f'  printf "finish\\n" > {shlex.quote(str(release))}\n'
        f'  read -r receipt < {shlex.quote(str(reaped))}\n'
        '  ;;\nesac\n'
        f'exec {real_cat} "$@"\n'
    )
    cat.chmod(0o755)
    # The process publishes its own end mark before exiting; the vanished case
    # deliberately leaves none. Reaping makes /proc disappearance deterministic.
    command = 'read -r finish < "$1"; '
    command += 'printf "0\\n" > "$2/rc.2"' if completed else 'exit 1'
    with subprocess.Popen(["bash", "-c", command, "runner", str(release), str(task)]) as runner:
        (task / "pid.2").write_text(f"{runner.pid}\n")
        stat = Path(f"/proc/{runner.pid}/stat")
        (task / "pidstart.2").write_text(stat.read_text().rsplit(") ", 1)[1].split()[19] + "\n")

        def reap():
            runner.wait(timeout=10)
            with reaped.open("w") as receipt:
                receipt.write("reaped\n")

        with ThreadPoolExecutor(max_workers=1) as pool:
            finished = pool.submit(reap)
            reply = subprocess.run(
                ["bash", "-c", file_node_bash.STATUS, "status", "task", "2"],
                env={**os.environ, "HOME": str(tmp_path), "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}"},
                capture_output=True, text=True, timeout=10, check=True,
            )
            finished.result(timeout=10)

    fields = dict(line.split("=", 1) for line in reply.stdout.splitlines())
    assert (fields["alive"], fields["rc"]) == ("0", "0" if completed else "")
