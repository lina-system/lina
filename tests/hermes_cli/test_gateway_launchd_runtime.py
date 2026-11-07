"""An unmanaged macOS gateway runs as a one-off launchd job through osascript, not as its caller's child.

Local Network Privacy attributes a process to its responsible app; a gateway left running after the
app that started it quits becomes responsible for itself and macOS refuses its LAN connects (a model
server on another machine answers ``No route to host``). A launchd-spawned osascript stays the
responsible code for the job's whole life (#71206).

Under test isolation the job is never bootstrapped (lina#372): the sandbox redirects HOME/HERMES_HOME
but launchd is account-wide, so a ``com.nousresearch.lina.runtime.<hex>`` job started by a test lands
in the developer's own ``gui/<uid>`` domain, outside the sandbox and out of the test's process
tracking. ``None`` sends both callers down their plain-child path instead.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
from types import SimpleNamespace

import pytest

from hermes_cli import gateway_launchd_runtime as runtime
from hermes_cli.gateway_launchd_runtime import start_gateway_job
from tests.waiting import wait_until


def _command_of(pid: int) -> str:
    return subprocess.run(["ps", "-o", "command=", "-p", str(pid)], capture_output=True, text=True, encoding="utf-8",
                          check=False, stdin=subprocess.DEVNULL).stdout.strip()


@pytest.mark.platforms("macos")
def test_gateway_job_is_a_launchd_osascript_child_that_reads_as_unsupervised(tmp_path, monkeypatch):
    # The real launch path: the conftest's test-isolation marker keeps a test run out of launchd (#372).
    monkeypatch.delenv("HERMES_TEST_ISOLATION", raising=False)
    report = tmp_path / "report.json"
    release = tmp_path / "release"
    child = (
        "import json, os, pathlib, time; "
        f"pathlib.Path({str(report)!r}).write_text(json.dumps({{'ppid': os.getppid(), "
        "'xpc': os.environ.get('XPC_SERVICE_NAME'), 'marker': os.environ.get('PROBE_MARKER')})); "
        f"[time.sleep(0.1) for _ in range(300) if not pathlib.Path({str(release)!r}).exists()]"
    )
    job = start_gateway_job([sys.executable, "-c", child], env={**os.environ, "PROBE_MARKER": "env-carried"},
                            cwd=tmp_path, log_path=tmp_path / "gateway-stdio.log")
    assert job is not None, "launchd refused the job in this user's GUI domain"
    try:
        try:
            wait_until(lambda: report.exists() or job.poll() is not None, timeout=30)
        except AssertionError:
            pass  # the assert below names what the job did instead
        log = tmp_path / "gateway-stdio.log"
        state = subprocess.run(["launchctl", "print", job.target], capture_output=True, text=True, encoding="utf-8",
                               check=False, stdin=subprocess.DEVNULL).stdout
        assert report.exists(), (f"tmp={tmp_path}\nlog={log.read_text(encoding='utf-8') if log.exists() else None!r}\n"
                                 f"launchctl print:\n{state[:3000]}")
        seen = json.loads(report.read_text(encoding="utf-8-sig"))
        wrapper = seen["ppid"]
        # The caller is not an ancestor: launchd (pid 1) spawned osascript, osascript the command.
        assert _command_of(wrapper).startswith("/usr/bin/osascript"), _command_of(wrapper)
        assert subprocess.run(["ps", "-o", "ppid=", "-p", str(wrapper)], capture_output=True, text=True, encoding="utf-8",
                              check=False, stdin=subprocess.DEVNULL).stdout.strip() == "1"
        assert seen["marker"] == "env-carried"
        # Read as unsupervised, or /restart would exit for a respawn no KeepAlive provides.
        assert seen["xpc"] == "0"
        assert not list(tmp_path.glob(".*.plist")), "the plist holding the launch env must not stay on disk"
        assert job.poll() is None
    finally:
        release.write_text("", encoding="utf-8")
    try:
        assert wait_until(lambda: job.poll() is not None, timeout=30)
        assert job.poll() == 0
    finally:
        # Unload this test's own job: under test isolation no later start sweeps finished jobs (#372).
        subprocess.run(["launchctl", "remove", job.target.rsplit("/", 1)[1]], capture_output=True,
                       check=False, stdin=subprocess.DEVNULL)


def _recording_launchctl(calls: list[tuple[str, ...]]):
    """A launchctl that answers like a healthy one and records every call (never touches launchd)."""
    def launchctl(*args, timeout):
        calls.append(args)
        return SimpleNamespace(returncode=0, stdout="", stderr="")
    return launchctl


@pytest.mark.platforms("posix")
def test_test_isolation_never_bootstraps_a_launchd_job(tmp_path, monkeypatch):
    """A gateway started under test isolation stays a plain child (lina#372).

    ``spawn_unmanaged_gateway`` reaches this on darwin and the respawn watcher on every macOS
    restart; launchd would take the job into the account's own ``gui/<uid>`` domain, where the
    sandbox ends and the test's ``Spawned`` tracking cannot see it. ``None`` is the function's
    existing "start it as a plain child" answer, so both callers already fall through.
    """
    calls: list[tuple[str, ...]] = []
    monkeypatch.setenv("HERMES_TEST_ISOLATION", str(tmp_path))
    monkeypatch.setattr(runtime, "_launchctl", _recording_launchctl(calls))

    job = start_gateway_job([sys.executable, "-c", "pass"], env={}, cwd=tmp_path,
                            log_path=tmp_path / "gateway-stdio.log")

    assert job is None, "test isolation must not hand the gateway to launchd"
    assert calls == [], f"no launchctl call at all, not even the finished-job sweep: {calls}"


@pytest.mark.platforms("posix")
def test_outside_test_isolation_the_gateway_is_still_a_launchd_job(tmp_path, monkeypatch):
    """The real macOS launch path is unchanged: no isolation marker, launchd still gets the job."""
    calls: list[tuple[str, ...]] = []
    monkeypatch.delenv("HERMES_TEST_ISOLATION", raising=False)
    monkeypatch.setattr(runtime, "_launchctl", _recording_launchctl(calls))

    job = start_gateway_job([sys.executable, "-c", "pass"], env={}, cwd=tmp_path,
                            log_path=tmp_path / "gateway-stdio.log")

    assert job is not None
    assert "bootstrap" in [call[0] for call in calls]
    bootstrap = next(call for call in calls if call[0] == "bootstrap")
    assert bootstrap[1] == f"gui/{os.getuid()}"  # windows-footgun: ok — posix-marked
    assert job.target.startswith(f"gui/{os.getuid()}/")  # windows-footgun: ok — posix-marked
