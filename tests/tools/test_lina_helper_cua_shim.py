"""The cua-driver wrapper the lina_helper provider launches (ported from clients/tests/test_hermes_cua_driver.py)."""

from __future__ import annotations

import io
import json
import os
import subprocess
from contextlib import redirect_stdout
from unittest import mock

import pytest

from tools.lina_helper import cua_driver_shim as w

REMOTE = "'/Applications/LINA Helper.app/Contents/Helpers/cua-driver'"
REMOTE_GATE = '"$HOME/Library/Application Support/LINA Helper/run/cua.sock"'


@pytest.fixture(autouse=True)
def gate(tmp_path, monkeypatch):
    path = str(tmp_path / "cua.sock")
    monkeypatch.setattr(w, "GATE", path)
    monkeypatch.setattr(w, "DRIVER", "/bin/echo")
    monkeypatch.setattr(w, "HOST", "")
    return path


def _exec(name, argv):
    with mock.patch.object(w.os, name, side_effect=SystemExit(0)) as ex, pytest.raises(SystemExit):
        w.main(argv)
    return ex.call_args[0]


def test_manifest_points_mcp_back_at_the_wrapper():
    upstream = {"binary_version": "0.38.0", "mcp_invocation": {"command": "/x/cua-driver", "args": ["mcp"]}}
    out = io.StringIO()
    done = subprocess.CompletedProcess([], 0, json.dumps(upstream), "")
    with mock.patch.object(w.subprocess, "run", return_value=done) as run, redirect_stdout(out):
        assert w.main(["manifest"]) == 0
    assert run.call_args[0][0] == ["/bin/echo", "manifest"]
    data = json.loads(out.getvalue())
    assert data["mcp_invocation"] == {"command": w.SELF, "args": ["mcp"]} and data["binary_version"] == "0.38.0"


def test_mcp_execs_driver_against_the_gate_and_drops_caller_endpoints(gate):
    open(gate, "w").close()
    path, argv = _exec("execv", ["mcp", "--no-overlay", "--socket", "/tmp/o.sock", "--direct", "--socket=/x", "--embedded"])
    assert path == "/bin/echo"
    assert argv == ["/bin/echo", "mcp", "--embedded", "--socket", gate, "--no-overlay"]


def test_mcp_remote_host_runs_the_proxy_on_that_mac(monkeypatch):
    monkeypatch.setattr(w, "HOST", "agent-mac")
    prog, argv = _exec("execvp", ["mcp", "--no-overlay", "--socket", "/x"])
    assert prog == "ssh" and argv[-2] == "agent-mac"
    assert argv[-1] == f"{REMOTE} mcp --embedded --socket {REMOTE_GATE} --no-overlay"


def test_bad_remote_host_missing_gate_or_driver_fail_without_exec(monkeypatch, tmp_path):
    with mock.patch.object(w.os, "execv") as ex, mock.patch.object(w.os, "execvp") as exp:
        assert w.main(["mcp"]) == 69  # no gate
        monkeypatch.setattr(w, "HOST", "-oProxyCommand=x")
        assert w.main(["mcp"]) == 69
        monkeypatch.setattr(w, "DRIVER", str(tmp_path / "nope"))
        assert w.main(["mcp"]) == 69
    ex.assert_not_called()
    exp.assert_not_called()


def test_call_and_status_go_to_the_gate_with_the_holder(gate, monkeypatch):
    open(gate, "w").close()
    for verb in ("call", "status"):
        assert _exec("execv", [verb, "get_screen_size", "{}", "--socket", "/x"])[1] == [
            "/bin/echo", verb, "--socket", gate, "get_screen_size", "{}"]
    monkeypatch.setenv("LINA_HELPER_CUA_HOLDER", "hermes-1")
    for args, want in (('{"pid":3}', {"pid": 3, "session": "hermes-1"}),
                       ('{"pid":3,"session":"other"}', {"pid": 3, "session": "other"})):
        assert json.loads(_exec("execv", ["call", "bring_to_front", args])[1][-1]) == want


def test_remote_call_and_passthrough(monkeypatch):
    assert _exec("execv", ["describe", "click"])[1] == ["/bin/echo", "describe", "click"]
    monkeypatch.setattr(w, "HOST", "agent-mac")
    assert _exec("execvp", ["call", "click", '{"x":1}', "--socket", "/x"])[1][-1] == (
        f"{REMOTE} call --socket {REMOTE_GATE} click '{{\"x\":1}}'")


def test_wrapper_is_executable():
    assert os.access(w.__file__, os.X_OK)
