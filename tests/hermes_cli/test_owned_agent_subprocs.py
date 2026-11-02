"""Long-lived agent/plugin subprocess clients die with the Hermes process that started them.

Each kill -9 case drives the REAL production spawn path in two disposable owner processes, SIGKILLs
one (no atexit, no finally: nothing of ours runs) and requires that owner's child tree to be gone
while the live owner's tree is untouched. On origin/main every one of these trees survived kill -9
forever: plain Popen children reparent to init, and detached ones lead their own session.
"""

import json
import os
import subprocess
import sys
from pathlib import Path

import psutil
import pytest

from hermes_cli import process_identity

from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]

# The child each production path starts: records itself, starts a grandchild that stays in its
# process group (the way a node bridge's helpers or a dev server's workers do), then idles.
_CHILD = r'''
import json, os, subprocess, sys, time
from pathlib import Path
import psutil
out = Path(sys.argv[1]); role = sys.argv[2]
p = psutil.Process()
(out / (role + '.tmp')).write_text(json.dumps({'pid': p.pid, 'created': p.create_time()}))
(out / (role + '.tmp')).replace(out / (role + '.json'))
if role == 'child':
    subprocess.Popen([sys.executable, __file__, str(out), 'grandchild'], stdin=subprocess.DEVNULL)
time.sleep(120)
'''

# Owner bodies: ``child`` is the argv starting the child, ``child_cmd`` the same as a shell string,
# ``out`` the receipt directory. Each starts the child through the production path; the owner idles.
_OWNERS = {
    # A dev server that never answers readiness: the owner dies mid-poll.
    "verify-app": (
        "from agent.verify import runner\n"
        "from agent.verify.recipes import Recipe\n"
        "threading.Thread(target=runner._run_start_phase, daemon=True,\n"
        "                 args=(Recipe(name='x', start=child_cmd, port=1), out, 120.0)).start()\n"
    ),
    # `git repack` (here a `git` on PATH that execs the child, the way repack forks pack-objects): the
    # owner dies inside the bounded wait.
    "git-maintenance": (
        "bindir = out / 'bin'; bindir.mkdir()\n"
        "(bindir / 'git').write_text('#!/bin/sh\\nexec ' + child_cmd + '\\n', encoding='utf-8')\n"
        "(bindir / 'git').chmod(0o755)\n"
        "os.environ['PATH'] = str(bindir) + os.pathsep + os.environ['PATH']\n"
        "from hermes_cli.git_commit_graph import run_bounded_git\n"
        "threading.Thread(target=run_bounded_git, args=(str(out), ['repack', '-d'], 120.0), daemon=True).start()\n"
    ),
    # A command provider that stalls: the owner dies inside its 120 s idle timeout.
    "tts-command": (
        "from tools.tts_command_provider import run_command_provider\n"
        "threading.Thread(target=run_command_provider, args=(child_cmd, 120.0), daemon=True).start()\n"
    ),
}


def _owner_source(site: str) -> str:
    return (
        "import os, shlex, sys, threading, time\n"
        "from pathlib import Path\n"
        "out = Path(sys.argv[1]); child_script = sys.argv[2]\n"
        "child = [sys.executable, child_script, str(out), 'child']\n"
        "child_cmd = ' '.join(shlex.quote(a) for a in child)\n"
        + _OWNERS[site]
        + "time.sleep(300)\n"
    )


def _read(path: Path) -> dict:
    wait_until(path.exists, timeout=30, what=f"receipt {path.name}")
    return json.loads(path.read_text(encoding="utf-8-sig"))


def _alive(identity: dict) -> bool:
    """Zombie-aware: a killed orphan lingers as a zombie until init reaps it."""
    try:
        proc = psutil.Process(identity["pid"])
        return proc.create_time() == identity["created"] and proc.status() != psutil.STATUS_ZOMBIE
    except psutil.NoSuchProcess:
        return False


def _start_owner(tmp_path: Path, name: str, site: str) -> tuple[subprocess.Popen, Path]:
    out = tmp_path / name
    out.mkdir()
    child_script = tmp_path / "child.py"
    child_script.write_text(_CHILD, encoding="utf-8")
    home = tmp_path / f"home-{name}"
    home.mkdir()
    env = dict(os.environ, HERMES_HOME=str(home / ".hermes"), HOME=str(home), HERMES_REAL_HOME=str(home),
               PYTHONPATH=str(REPO))
    with (tmp_path / f"{name}.err").open("w", encoding="utf-8") as err:
        owner = subprocess.Popen([sys.executable, "-c", _owner_source(site), str(out), str(child_script)],
                                 env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=err)
    return owner, out


# The children reparent to init once their owner dies, so cleaning them up leaves this test's
# subtree; real signal delivery to a real orphan is the point of the test.
@pytest.mark.live_system_guard_bypass
@pytest.mark.parametrize("site", sorted(_OWNERS))
def test_a_sigkilled_owner_takes_its_child_tree_and_spares_a_live_owners(tmp_path, site):
    owners: dict = {}
    ids: dict = {}
    try:
        for name in ("dead", "live"):
            owners[name], out = _start_owner(tmp_path, name, site)
            ids[name] = {role: _read(out / f"{role}.json") for role in ("child", "grandchild")}
        assert all(_alive(i) for tree in ids.values() for i in tree.values())

        owners["dead"].kill()  # SIGKILL: no atexit, no finally, nothing of ours runs
        owners["dead"].wait(timeout=10)

        wait_until(lambda: not any(_alive(i) for i in ids["dead"].values()), timeout=15,
                   what=f"the {site} tree to die with its kill -9'd owner")
        assert all(_alive(i) for i in ids["live"].values()), f"a live owner's {site} tree was killed"
    finally:
        for tree in ids.values():
            for identity in tree.values():
                if _alive(identity):
                    psutil.Process(identity["pid"]).kill()
        for owner in owners.values():
            if owner.poll() is None:
                owner.kill()
            owner.wait(timeout=10)


def _sleeper() -> subprocess.Popen:
    return subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"], stdin=subprocess.DEVNULL,
                            start_new_session=True)


@pytest.fixture
def owned(monkeypatch):
    """In-process cases: record registrations instead of starting a real supervisor in pytest."""
    from hermes_cli import process_identity

    groups: list = []
    monkeypatch.setattr(process_identity, "own_process_groups", lambda pgids: groups.extend(pgids))
    monkeypatch.setattr(process_identity, "register_child", lambda pid, purpose: None)
    return groups


def _reap(proc: subprocess.Popen) -> None:
    if proc.poll() is None:
        proc.kill()
    proc.wait(timeout=10)


def test_verify_start_phase_owns_then_releases_the_app_group(monkeypatch, tmp_path, owned):
    from agent.verify import runner
    from agent.verify.recipes import Recipe

    released = []
    monkeypatch.setattr(process_identity, "release_process_groups", lambda pgids: released.append(list(pgids)))
    result = runner._run_start_phase(Recipe(name="x", start="sleep 30", port=1), tmp_path, 0.3)
    assert not result.ready
    assert len(owned) == 1 and released == [owned]


def test_tts_command_owns_then_releases_its_group_after_success_and_idle_kill(monkeypatch, owned):
    from tools import tts_command_provider as tts

    released = []
    monkeypatch.setattr(process_identity, "release_process_groups", lambda pgids: released.extend(pgids))
    assert tts.run_command_provider("echo hi", 10.0).stdout.strip() == "hi"
    with pytest.raises(subprocess.TimeoutExpired):
        tts.run_command_provider("sleep 30", 0.3)
    assert len(owned) == 2 and released == owned


def test_whatsapp_pairing_terminate_releases_the_process_it_stopped(monkeypatch):
    from hermes_cli.web_routers import messaging

    released = []
    monkeypatch.setattr(process_identity, "release_process_groups", lambda pgids: released.append(list(pgids)))
    pairing = _sleeper()
    try:
        messaging._terminate_whatsapp_pairing(pairing)
        assert pairing.poll() is not None, "_terminate_whatsapp_pairing() left the process running"
        assert released == [[pairing.pid]]
    finally:
        _reap(pairing)
