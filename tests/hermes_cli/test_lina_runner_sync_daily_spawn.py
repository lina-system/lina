"""The serve tick's real start of the daily runner sync (lina#238): ``maybe_start`` -> ``_spawn`` -> ``_owned`` ->
``process_identity.spawn_owned`` starts a real child that carries the day's start as ``--daily-run``, and that child's
``runners sync`` records its end in ``daily.json`` under that start.

Only the interpreter is replaced: ``sys.executable`` is a harness that writes its argv, then runs the real ``runners``
parser and ``_cmd_runners`` (in place of ``hermes_cli.main``'s top-level dispatch) with the fake Grok source, no
nodes, no fetch, no notice and a fixed clock, so nothing leaves the machine.
"""

from __future__ import annotations

import json
import sys

import pytest

from hermes_cli import lina_runner_sync_daily
from hermes_cli import lina_runner_sync_state as state
from hermes_cli.process_identity import release_process_group_once_empty
from tests.hermes_cli.test_lina_runner_sync import make_hub
from tests.hermes_cli.test_lina_runner_sync_node import _local

HARNESS = """#!{python}
import argparse, json, os, sys
from pathlib import Path

args = sys.argv[1:]
Path(os.environ["SMOKE_ARGV"]).write_text(json.dumps(args), encoding="utf-8")
if args[:3] != ["-P", "-m", "hermes_cli.main"]:
    sys.exit(f"not the daily sync's command: {{args}}")

from hermes_cli import lina_deps, lina_runner_sync
from hermes_cli.subcommands import runners
from tests.hermes_cli.test_lina_runner_sync import FakeHttp, release

root = Path(os.environ["SMOKE_ROOT"])
lina_deps.lina_root = lambda: root
lina_deps.platform_key = lambda: "linux-x64"
http = FakeHttp()
http.publish("1.0.71", release("1.0.71"))
followed, real_env = lina_runner_sync.followed, lina_runner_sync.Env


def no_fetch(url, dest):
    raise AssertionError(f"unexpected fetch {{url}}")


lina_runner_sync.followed = lambda base, only=None: followed(base, only or ["grok"])
lina_runner_sync.Env = lambda: real_env(http=http, fetch=no_fetch, nodes=lambda base: [],
                                        sign_in=lambda runner: "signed_in", notify=lambda base, text: "kept",
                                        clock=lambda: float(os.environ["SMOKE_NOW"]))
parser = argparse.ArgumentParser(prog="lina")
runners.build_runners_parser(parser.add_subparsers(dest="command"))
runners._cmd_runners(parser.parse_args(args[3:]))
"""


@pytest.mark.platforms("posix")
def test_the_real_start_hands_the_daily_start_to_its_runners_sync_child(tmp_path, monkeypatch):
    root = make_hub(tmp_path, monkeypatch)["root"]
    (root / "config.yaml").write_text('worker:\n  runner_sync: {at: "04:00"}\n', encoding="utf-8")
    harness = tmp_path / "python"
    harness.write_text(HARNESS.format(python=sys.executable), encoding="utf-8")
    harness.chmod(0o755)
    argv_file = tmp_path / "argv.json"
    now, ended = _local(0, 4, 0), _local(0, 4, 7)
    monkeypatch.setenv("SMOKE_ROOT", str(root))
    monkeypatch.setenv("SMOKE_ARGV", str(argv_file))
    monkeypatch.setenv("SMOKE_NOW", repr(ended))
    monkeypatch.setattr(sys, "executable", str(harness))
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)

    assert lina_runner_sync_daily.maybe_start(root / "worker", now=now) is True
    child = lina_runner_sync_daily._child
    try:
        assert child.wait(timeout=120) == 0, (state.sync_dir(root) / "sync.log").read_text(encoding="utf-8")
    finally:
        release_process_group_once_empty(child.pid, proc=child)

    assert json.loads(argv_file.read_text(encoding="utf-8")) == [
        "-P", "-m", "hermes_cli.main", "runners", "sync", "--daily-run", repr(now)]
    daily = state.daily(root)
    assert (daily["started"], daily["finished"], daily["rc"]) == (now, ended, 0)
    assert state.last(root)["started"] == ended
    assert "grok" in (state.sync_dir(root) / "sync.log").read_text(encoding="utf-8")
