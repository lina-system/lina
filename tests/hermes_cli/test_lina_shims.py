"""The ``work`` caller shim (lina-system/lina#37): ``work`` -> ``lina work``; ``omo-task`` is gone (lina#434).

Every verb and option of ``work`` is routed and then parsed by the real ``lina`` parser; the installed shim runs as
a real process against a temp LINA home: stdout is the wrapped command's and exit codes pass through.
``lina install --shims`` writes only ``work`` and removes the ``omo-task`` shim an older install wrote, never a
file of that name it did not write.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys

import pytest

from hermes_cli import lina_shims
from hermes_cli.subcommands.work import build_work_parser


def _parse(argv: list[str]) -> argparse.Namespace:
    root = argparse.ArgumentParser(prog="lina")
    sub = root.add_subparsers(dest="command")
    build_work_parser(sub)
    return root.parse_args(argv)


@pytest.fixture
def base(tmp_path):
    root = tmp_path / "lina"
    root.mkdir()
    return root


WORK = [
    (["add", "todo", "--by", "sion", "--goal", "G-1", "--title", "x", "--why", "y"], {"cmd": "add", "kind": "todo"}),
    (["note", "T-1", "done the part"], {"cmd": "note", "id": "T-1", "text": "done the part"}),
    (["wait", "T-1", "--on", "owner"], {"cmd": "wait", "on": "owner"}),
    (["evidence", "T-1", "pr:1"], {"cmd": "evidence", "refs": ["pr:1"]}),
    (["confirm", "T-1", "--quote", "ok"], {"cmd": "confirm", "quote": "ok"}),
    (["list", "--by", "sion"], {"cmd": "list", "by": "sion"}),
    (["show", "G-1"], {"cmd": "show", "id": "G-1"}),
    (["json", "--all"], {"cmd": "json", "all": True}),
    (["check", "--all"], {"cmd": "check", "all": True}),
    (["run", "submit", "T-1", "--brief", "b.md"], {"cmd": "run", "run_action": "submit", "todos": ["T-1"]}),
    (["run", "status", "OT-SIO1", "--json"], {"cmd": "run", "run_action": "status", "json": True}),
]


@pytest.mark.parametrize(("args", "expected"), WORK, ids=[" ".join(a[:2]) for a, _ in WORK])
def test_every_work_verb_reaches_lina_work_unchanged(base, args, expected):
    routed = lina_shims.route("work", args, base)
    assert routed.to == "lina" and routed.argv == ["work", *args]
    parsed = vars(_parse(routed.argv))
    assert {key: parsed[key] for key in expected} == expected


def test_omo_task_is_no_shim_any_more(base):
    with pytest.raises(ValueError, match="no shim 'omo-task'"):
        lina_shims.route("omo-task", ["status", "OT-SIO1"], base)


def _run(shim, args, base):
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(base.parent), "HERMES_HOME": str(base),
           "LANG": "C.UTF-8"}
    return subprocess.run([str(shim), *args], capture_output=True, text=True, env=env, timeout=120, check=False)


def _direct(args, base):
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(base.parent), "HERMES_HOME": str(base),
           "LANG": "C.UTF-8", "PYTHONPATH": str(lina_shims.TEMPLATES.parents[1])}
    return subprocess.run([sys.executable, "-m", "hermes_cli.main", *args], capture_output=True, text=True, env=env,
                          timeout=120, check=False)


@pytest.mark.platforms("posix")
def test_the_installed_work_shim_prints_what_lina_work_prints_and_passes_its_exit_code(base):
    assert lina_shims.install(base, out=lambda line: None) == 0
    for args in (["json"], ["show", "G-NOPE"]):
        through, direct = _run(base / "bin" / "work", args, base), _direct(["work", *args], base)
        assert (through.returncode, through.stdout) == (direct.returncode, direct.stdout)


@pytest.mark.platforms("posix")
def test_install_writes_only_the_work_shim(base):
    lines: list[str] = []
    assert lina_shims.install(base, out=lines.append) == 0
    assert sorted(p.name for p in (base / "bin").iterdir()) == ["work"]
    assert len(lines) == 1 and "omo-task" not in lines[0]


@pytest.mark.platforms("posix")
def test_install_removes_the_omo_task_shim_an_older_install_wrote(base):
    old = base / "bin" / "omo-task"
    old.parent.mkdir(parents=True)
    old.write_text("#!/bin/sh\n# omo-task -> lina worker, same verbs and options (lina#37). Written by "
                   "`lina install --shims`;\nexec true\n", encoding="utf-8")
    lines: list[str] = []
    assert lina_shims.install(base, out=lines.append) == 0
    assert not old.exists()
    assert sorted(p.name for p in (base / "bin").iterdir()) == ["work"]
    assert [line for line in lines if "omo-task" in line] == [
        f"shims: removed the old omo-task shim from {base / 'bin'}; call `lina worker` instead."]


@pytest.mark.platforms("posix")
def test_install_leaves_a_foreign_omo_task_file_alone_and_names_it(base):
    foreign = base / "bin" / "omo-task"
    foreign.parent.mkdir(parents=True)
    foreign.write_text("#!/bin/sh\nexec /opt/old/omo-task \"$@\"\n", encoding="utf-8")
    foreign.chmod(0o755)
    before = (foreign.read_bytes(), foreign.stat().st_mode)
    lines: list[str] = []
    assert lina_shims.install(base, out=lines.append) == 0
    assert (foreign.read_bytes(), foreign.stat().st_mode) == before
    assert [line for line in lines if "omo-task" in line] == [
        f"shims: left {foreign} alone: it was not written by `lina install --shims`."]


@pytest.mark.platforms("posix")
def test_lina_install_shims_runs_the_same_install_from_the_cli(base):
    old = base / "bin" / "omo-task"
    old.parent.mkdir(parents=True)
    old.write_text("# Written by `lina install --shims`;\n", encoding="utf-8")
    env = {"PATH": os.environ.get("PATH", "/usr/bin:/bin"), "HOME": str(base.parent), "LINA_HOME": str(base),
           "LANG": "C.UTF-8", "PYTHONPATH": str(lina_shims.TEMPLATES.parents[1])}
    done = subprocess.run([sys.executable, "-m", "hermes_cli.main", "install", "--shims"], capture_output=True,
                          text=True, env=env, timeout=120, check=False)
    assert done.returncode == 0, done.stderr
    assert sorted(p.name for p in (base / "bin").iterdir()) == ["work"]
    assert "removed the old omo-task shim" in done.stdout
