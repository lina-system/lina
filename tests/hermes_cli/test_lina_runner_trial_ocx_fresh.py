"""The OCX check of a runner trial and probe reads the OCX log fresh (lina#238): with ``ocx.external`` every other read
is kept 60 s, and a trial that finished inside that window used to compare the log with itself and hold the runner on
``(0 new entries)``. Other reads keep the cache.

The external OCX is a fake ``ocx`` whose log gains one ``responses`` 200 per ``logs --json`` call, as a real OCX's log
grows with each request; the runner is a fake that prints ``OK``, run through the real codex pin's trial.
"""
import json
import os
import stat
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_models, lina_ocx, lina_runner_probe, lina_runners
from hermes_cli import lina_runner_sync_trial as trials

OCX = f"""#!{sys.executable}
import json, sys, time
from pathlib import Path
here = Path(__file__).parent
args = sys.argv[1:]
if args == ["status", "--json"]:
    print(json.dumps({{"proxy": {{"running": True}}}})); sys.exit(0)
name = "-".join(a for a in args if not a.startswith("--"))
calls = here / ("calls-" + name)
n = int(calls.read_text()) + 1 if calls.exists() else 1
calls.write_text(str(n))
if args == ["account", "list", "--json"]:
    print(json.dumps({{"accounts": []}})); sys.exit(0)
if args == ["logs", "--json"]:
    if str(n) in (here / "fail").read_text().split():
        print("log store locked", file=sys.stderr); sys.exit(3)
    now = int(time.time() * 1000)
    rows = [{{"requestId": "old-%d" % i, "timestamp": now - 600000, "status": 200, "inboundProtocol": "chat"}}
            for i in range(3)]
    rows += [{{"requestId": "new-%d" % k, "timestamp": now, "status": 200, "inboundProtocol": "responses"}}
             for k in range(2, n + 1)]
    print(json.dumps({{"logs": rows, "total": len(rows)}})); sys.exit(0)
sys.exit(2)
"""


@pytest.fixture
def ocx(tmp_path, monkeypatch):
    work = tmp_path / "ocx"
    work.mkdir()
    (work / "fail").write_text("")
    cli = work / "ocx"
    cli.write_text(OCX)
    runner = work / "codex"
    runner.write_text("#!/bin/sh\necho OK\n")
    for f in (cli, runner):
        f.chmod(f.stat().st_mode | stat.S_IXUSR)
    root = tmp_path / "root"
    root.mkdir()
    monkeypatch.setattr(lina_models, "_CACHE", {})
    monkeypatch.setattr(lina_models, "_STALE", {})
    monkeypatch.setattr(lina_ocx, "external", lambda root=None: {"cli": str(cli)})
    monkeypatch.setattr(lina_runners, "command", lambda root, pin, version="current": [str(runner)])
    monkeypatch.setattr(lina_runners, "run_env", lambda root, pin: dict(os.environ))
    monkeypatch.setattr(lina_runners, "current_version", lambda root, pin: "0.167.1")
    return root, work


def calls(work: Path, name: str) -> int:
    path = work / f"calls-{name}"
    return int(path.read_text()) if path.exists() else 0


@pytest.mark.platforms("posix")
def test_a_trial_inside_the_cache_window_sees_its_own_200(ocx):
    root, work = ocx
    assert trials.trial(root, lina_runners.pins()["codex"], "0.167.1") == (True, "OCX answered 200")
    assert calls(work, "logs") == 2


@pytest.mark.platforms("posix")
def test_the_daily_probe_inside_the_cache_window_passes(ocx):
    root, work = ocx
    result = lina_runner_probe.probe(root, lina_runners.pins()["codex"])
    assert (result["state"], result["why"]) == ("done", "OCX answered 200")
    assert calls(work, "logs") == 2


@pytest.mark.platforms("posix")
def test_other_reads_of_the_external_ocx_stay_cached(ocx):
    root, work = ocx
    trials.trial(root, lina_runners.pins()["codex"], "0.167.1")
    first = lina_models.query("accounts", root)
    assert lina_models.query("accounts", root) == first
    assert lina_models.run(("logs", "--json"), root) == lina_models.run(("logs", "--json"), root)
    assert (calls(work, "account-list"), calls(work, "logs")) == (1, 2)


@pytest.mark.platforms("posix")
@pytest.mark.parametrize(("failing", "why"), [
    ("1", "OCX log could not be read before the trial: ocx logs --json: log store locked"),
    ("2", "answered OK but the OCX log could not be read: ocx logs --json: log store locked"),
])
def test_an_unreadable_log_still_fails_the_trial_with_its_cause(ocx, failing, why):
    root, work = ocx
    (work / "fail").write_text(failing)
    assert trials.trial(root, lina_runners.pins()["codex"], "0.167.1") == (False, why)
