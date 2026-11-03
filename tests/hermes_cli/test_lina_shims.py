"""The caller shims (lina-system/lina#37): ``omo-task`` -> ``lina worker`` and ``work`` -> ``lina work``.

Every verb and option of the old tools is routed and then parsed by the real ``lina`` parsers; the installed shims
run as real processes against a temp LINA home: stdout is the wrapped command's, exit codes pass through, an ID that
``lina worker`` did not start goes to the old tool named in ``worker.legacy_omo_task``, one it did start never does.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import textwrap

import pytest

from hermes_cli import lina_shims
from hermes_cli.subcommands.work import build_work_parser
from hermes_cli.subcommands.worker import build_worker_parser


def _parse(argv: list[str]) -> argparse.Namespace:
    root = argparse.ArgumentParser(prog="lina")
    sub = root.add_subparsers(dest="command")
    build_worker_parser(sub)
    build_work_parser(sub)
    return root.parse_args(argv)


@pytest.fixture
def base(tmp_path):
    root = tmp_path / "lina"
    root.mkdir()
    return root


OMO_TASK = [
    (["submit", "--brief", "b.md", "--mode", "research", "--repo", "o/n", "--input", "in.txt", "--todo", "T-1",
      "--host", "build-a", "--model", "opus", "--topic", "t", "--bot", "sion", "--at", "2026-11-03 09:00",
      "--every", "1d", "--wait", "60"],
     {"worker_action": "submit", "brief": ["b.md"], "mode": "research", "repo": "o/n", "input": ["in.txt"],
      "todo": "T-1", "host": "build-a", "model": "opus", "topic": "t", "bot": "sion", "at": "2026-11-03 09:00",
      "every": "1d", "wait": 60}),
    (["submit", "--brief", "b.md", "--mode", "mass-research", "--wait"], {"mode": "mass-research", "wait": 1800}),
    (["status", "OT-SIO1"], {"worker_action": "status", "id": "OT-SIO1"}),
    (["list"], {"worker_action": "list", "all": False}),
    (["send", "OT-SIO1", "also the docs"], {"worker_action": "send", "id": "OT-SIO1", "message": "also the docs"}),
    (["cancel", "OT-SIO1"], {"worker_action": "cancel", "id": "OT-SIO1"}),
    (["result", "OT-SIO1"], {"worker_action": "result", "id": "OT-SIO1"}),
    (["accept", "OT-SIO1", "--note", "checked"], {"worker_action": "accept", "id": "OT-SIO1", "note": "checked"}),
    (["reject", "OT-SIO1", "--why", "no test"], {"worker_action": "reject", "id": "OT-SIO1", "why": "no test"}),
    (["receipts", "OT-SIO1"], {"worker_action": "receipts", "id": "OT-SIO1"}),
    (["hosts"], {"worker_action": "hosts"}),
    (["schedules"], {"worker_action": "schedules"}),
    (["unschedule", "OS-SIO1"], {"worker_action": "unschedule", "id": "OS-SIO1"}),
]

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


@pytest.mark.parametrize(("args", "expected"), OMO_TASK, ids=[" ".join(a[:2]) for a, _ in OMO_TASK])
def test_every_omo_task_verb_and_option_reaches_lina_worker(base, args, expected):
    routed = lina_shims.route("omo-task", args, base)
    assert routed.to == "lina" and routed.argv == ["worker", *args]
    parsed = vars(_parse(routed.argv))
    assert {key: parsed[key] for key in expected} == expected


@pytest.mark.parametrize(("args", "expected"), WORK, ids=[" ".join(a[:2]) for a, _ in WORK])
def test_every_work_verb_reaches_lina_work_unchanged(base, args, expected):
    routed = lina_shims.route("work", args, base)
    assert routed.to == "lina" and routed.argv == ["work", *args]
    parsed = vars(_parse(routed.argv))
    assert {key: parsed[key] for key in expected} == expected


def _with_legacy(base, tmp_path) -> str:
    legacy = tmp_path / "old-omo-task"
    legacy.write_text(textwrap.dedent(f"""\
        #!{sys.executable}
        import json, sys
        print(json.dumps({{"legacy": sys.argv[1:]}}))
        sys.exit(7)
        """), encoding="utf-8")
    legacy.chmod(0o755)
    (base / "config.yaml").write_text(f"worker:\n  legacy_omo_task: {legacy}\n", encoding="utf-8")
    return str(legacy)


def _own(base, task_id: str) -> None:
    jobs = base / "worker" / "hub" / "jobs"
    jobs.mkdir(parents=True, exist_ok=True)
    (jobs / f"{task_id}.json").write_text(json.dumps({"id": task_id}), encoding="utf-8")


@pytest.mark.parametrize("verb", sorted(lina_shims.ID_VERBS))
def test_an_id_lina_did_not_start_goes_to_the_old_tool_and_one_it_did_never_does(base, tmp_path, verb):
    legacy = _with_legacy(base, tmp_path)
    _own(base, "OT-NEW1")
    assert lina_shims.route("omo-task", [verb, "OT-OLD1"], base).argv == [legacy, verb, "OT-OLD1"]
    routed = lina_shims.route("omo-task", [verb, "OT-NEW1"], base)
    assert routed.to != "legacy"
    assert lina_shims.route("omo-task", ["list"], base).to == "lina"


def test_fork_of_a_lina_task_is_refused_and_without_a_legacy_tool_every_id_is_lina(base):
    assert lina_shims.route("omo-task", ["fork", "OT-NEW1"], base).to == "refuse"
    assert lina_shims.route("omo-task", ["status", "OT-OLD1"], base).to == "lina"


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
def test_installed_shims_print_what_lina_prints_and_pass_its_exit_code(base, tmp_path):
    assert lina_shims.install(base, out=lambda line: None) == 0
    shim = base / "bin" / "omo-task"
    for args in (["hosts"], ["status", "OT-NOPE1"]):
        through, direct = _run(shim, args, base), _direct(["worker", *args], base)
        assert (through.returncode, through.stdout, through.stderr) == (direct.returncode, direct.stdout,
                                                                       direct.stderr)
    assert _run(shim, ["status", "OT-NOPE1"], base).returncode == 2
    work = _run(base / "bin" / "work", ["json"], base)
    assert (work.returncode, work.stdout) == (_direct(["work", "json"], base).returncode,
                                             _direct(["work", "json"], base).stdout)
    fork = _run(shim, ["fork", "OT-NOPE1"], base)
    assert fork.returncode == 2 and fork.stdout == "" and "no fork" in fork.stderr


@pytest.mark.platforms("posix")
def test_an_installed_shim_hands_an_old_id_to_the_old_tool_with_its_argv_and_exit(base, tmp_path):
    _with_legacy(base, tmp_path)
    assert lina_shims.install(base, out=lambda line: None) == 0
    done = _run(base / "bin" / "omo-task", ["accept", "OT-OLD1", "--note", "fine"], base)
    assert done.returncode == 7
    assert json.loads(done.stdout) == {"legacy": ["accept", "OT-OLD1", "--note", "fine"]}
