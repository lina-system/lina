"""LINA Helper core tools through real registry dispatch against a temp HERMES_HOME device list.

Only ``subprocess.run`` is faked: names resolve from config.yaml ``devices``, unknown names and bad hosts are
refused before any process runs, and the argv that would run is the fixed lina-helperctl / unlock command.
"""

from __future__ import annotations

import json
import os
import shlex
import subprocess

import pytest

import hermes_cli.config as hc
import hermes_yaml as yaml
import tools.lina_helper.tool
from tools.lina_helper import actions, client, unlock
from tools.registry import registry
from toolsets import _HERMES_CORE_TOOLS, TOOLSETS

CTL = client.CTL
TOOLS = ["mac_use", "mac_status", "mac_screenshot", "mac_unlock_status", "mac_unlock", "mac_unlock_revoke"]
DEVICES = {"default_mac": "agent-hub",
           "macs": {"agent-hub": {"host": "local"}, "agent-mac": {"host": "box-1", "unlock": True},
                    "laptop-1": {"host": "box-2"}, "evil": {"host": "-oProxyCommand=x"}}}


def ok(result=None) -> bytes:
    return (json.dumps({"v": 1, "ok": True, "result": result or {}}) + "\n").encode()


def err(code, detail=None) -> bytes:
    e = {"code": code, **({"detail": detail} if detail else {})}
    return (json.dumps({"v": 1, "ok": False, "error": e}) + "\n").encode()


class Run:
    def __init__(self, stdout=b"", stderr=b"", returncode=0, exc=None, by_action=None):
        self.result = subprocess.CompletedProcess([], returncode, stdout or ok(), stderr)
        self.exc, self.by_action, self.calls = exc, by_action or {}, []

    def __call__(self, cmd, **kw):
        self.calls.append((cmd, kw))
        if self.exc:
            raise self.exc
        remote = shlex.split(cmd[-1]) if cmd[0] == "ssh" else cmd
        return self.by_action.get(remote[1] if remote[0] == CTL else remote[-1], self.result)


def remote_of(cmd):
    return shlex.split(cmd[-1]) if cmd[0] == "ssh" else cmd


@pytest.fixture
def home(monkeypatch):
    path = os.environ["HERMES_HOME"]

    def write(devices):
        with open(os.path.join(path, "config.yaml"), "w") as f:
            f.write(yaml.safe_dump({"devices": devices} if devices is not None else {}))
        hc._LOAD_CONFIG_CACHE.clear()
    write(DEVICES)
    actions.targets._by_session.clear()
    yield write
    hc._LOAD_CONFIG_CACHE.clear()
    actions.targets._by_session.clear()


@pytest.fixture
def run(monkeypatch, home):
    fake = Run()
    monkeypatch.setattr(actions.subprocess, "run", fake)
    return fake


@pytest.fixture
def released(monkeypatch):
    got = []
    monkeypatch.setattr(actions, "release_backend", got.append)
    return got


def call(name, args, session="sess-1"):
    return json.loads(registry.dispatch(name, args, session_id=session))


def test_tools_are_core_and_in_the_lina_helper_toolset():
    for name in TOOLS:
        assert registry.get_entry(name).toolset == "lina_helper"
        assert name in _HERMES_CORE_TOOLS
    assert sorted(TOOLSETS["lina_helper"]["tools"]) == sorted(TOOLS)


@pytest.mark.platforms("linux")
def test_check_fn_follows_the_device_list(home):
    home(None)
    assert not tools.lina_helper.tool.helper_available()
    assert not tools.lina_helper.tool.unlock_available()
    home({"macs": {"agent-hub": {"host": "local", "unlock": True}}})
    assert tools.lina_helper.tool.helper_available()
    assert not tools.lina_helper.tool.unlock_available()  # unlock reaches another Mac only
    home(DEVICES)
    assert tools.lina_helper.tool.unlock_available()


def test_local_runs_ctl_directly_and_remote_uses_fixed_ssh(run):
    out = call("mac_status", {"mac": "agent-hub"})
    assert out["ok"] and out["mac"] == "agent-hub" and out["computer_use_target"] == "agent-hub"
    assert [c[0] for c in run.calls] == [[CTL, "status"], [CTL, "cua.status"]]
    run.calls.clear()
    call("mac_status", {"mac": "agent-mac"})
    assert run.calls[0][0] == ["ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
                               "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2", "box-1",
                               shlex.join([CTL, "status"])]


@pytest.mark.parametrize("mac", ["nope", 5, "box-1", "-oProxyCommand=x", "agent-hub "])
def test_unknown_mac_is_refused_before_any_process(run, mac):
    out = call("mac_status", {"mac": mac})
    assert run.calls == []
    assert out["ok"] is False and out["error"]["code"] == "unknown_mac"


def test_bad_host_in_config_never_reaches_ssh(run):
    out = call("mac_status", {"mac": "evil"})
    assert run.calls == [] and out["error"]["code"] == "bad_request"


def test_mac_use_switches_the_session_target_and_restarts_its_connection(run, released):
    out = call("mac_use", {"mac": "agent-mac"})
    assert out["ok"] and out["previous"] == "agent-hub" and released == ["sess-1"]
    assert run.calls == []
    call("mac_status", {})
    assert run.calls[0][0][-2] == "box-1"
    assert actions.targets.get("other") == "agent-hub"
    call("mac_use", {"mac": "agent-mac"})
    assert released == ["sess-1"]  # same target: no restart
    for bad in ["nope", None, 5]:
        assert call("mac_use", {"mac": bad})["ok"] is False
    assert actions.targets.get("sess-1") == "agent-mac"


def test_mac_use_target_is_scoped_by_profile_as_well_as_session(run, tmp_path):
    # Two profiles in one multiplexed process can share a session id; one's choice must not leak (lina#60).
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override
    for name in ("profile-a", "profile-b"):
        (tmp_path / name).mkdir()
        (tmp_path / name / "config.yaml").write_text(yaml.safe_dump({"devices": DEVICES}))
    hc._LOAD_CONFIG_CACHE.clear()
    token = set_hermes_home_override(str(tmp_path / "profile-a"))
    try:
        actions.targets.set("sess-1", "agent-mac")
        assert actions.targets.get("sess-1") == "agent-mac"
    finally:
        reset_hermes_home_override(token)
    token = set_hermes_home_override(str(tmp_path / "profile-b"))
    try:
        assert actions.targets.get("sess-1") == "agent-hub"
    finally:
        reset_hermes_home_override(token)


def test_screenshot_lands_in_the_profile_cache(run):
    png = b"\x89PNG\r\n\x1a\nbytes"
    run.result = subprocess.CompletedProcess([], 0, png, ok({"width": 800, "height": 600, "path": "/remote/x.png"}))
    out = call("mac_screenshot", {"mac": "agent-mac", "display": 2})
    cache = os.path.join(os.environ["HERMES_HOME"], "cache", "lina-helper")
    assert out["ok"] and os.path.dirname(out["path"]) == cache
    assert (out["width"], out["height"], out["bytes"]) == (800, 600, len(png))
    with open(out["path"], "rb") as f:
        assert f.read() == png
    assert oct(os.stat(cache).st_mode & 0o777) == "0o700"
    argv = remote_of(run.calls[0][0])
    assert json.loads(argv[argv.index("--json") + 1]) == {"display": 2} and argv[-2:] == ["--out", "-"]
    assert "/remote/" not in json.dumps(out)


@pytest.mark.parametrize("display", ["second", 0, True])
def test_screenshot_bad_display_is_refused(run, display):
    out = call("mac_screenshot", {"mac": "agent-hub", "display": display})
    assert run.calls == [] and out["error"]["code"] == "bad_request"


def test_helper_errors_are_structured(run):
    run.result = subprocess.CompletedProcess([], 1, err("permission_missing", "screen_recording"), b"")
    assert call("mac_status", {"mac": "agent-hub"})["error"] == {"code": "permission_missing",
                                                                 "detail": "screen_recording"}
    run.result = subprocess.CompletedProcess([], 255, b"", b"ssh: Could not resolve hostname box-1\n")
    out = call("mac_status", {"mac": "agent-mac"})
    assert out["error"]["code"] == "ssh_failed" and "Could not resolve" in out["error"]["detail"]
    run.result = subprocess.CompletedProcess([], 0, b"hello\n", b"")
    assert call("mac_status", {"mac": "agent-hub"})["error"]["code"] == "invalid_remote_response"
    run.exc = subprocess.TimeoutExpired("ssh", 30)
    assert call("mac_status", {"mac": "agent-mac"})["error"]["code"] == "timeout"
    assert len(run.calls) == 4


def _unlock_reply(**over):
    value = {"schema_version": 1, "result": "unlocked", "desktop_ready": True, "screen_unlocked": True,
             "authorization_consumed": True, "legacy_keychain_status": 0, "data_protection_status": 0,
             "active_displays": 1, **over}
    return subprocess.CompletedProcess([], 0, json.dumps(value).encode(), b"")


def test_unlock_runs_the_fixed_sudo_command_on_an_enabled_mac(run):
    run.result = _unlock_reply()
    out = call("mac_unlock", {"mac": "agent-mac"})
    assert out["success"] and out["desktop_ready"] and out["mac"] == "agent-mac"
    cmd, kw = run.calls[0]
    assert cmd[-2] == "box-1" and shlex.split(cmd[-1]) == ["sudo", "-n", unlock.CTL, "unlock"]
    assert kw["timeout"] == 25 and kw["stdin"] is subprocess.DEVNULL


@pytest.mark.parametrize("key,bad", [("data_protection_status", -25308), ("authorization_consumed", False),
                                     ("active_displays", 0), ("active_displays", "1"),
                                     ("legacy_keychain_status", False)])
def test_unlock_success_requires_every_postcondition(run, key, bad):
    run.result = _unlock_reply(**{key: bad})
    out = call("mac_unlock", {"mac": "agent-mac"})
    assert out["success"] is False and out["desktop_ready"] is False


@pytest.mark.parametrize("args,code", [({"mac": "laptop-1"}, "not_allowed"), ({"mac": "nope"}, "unknown_mac"),
                                       ({"mac": "agent-mac", "host": "other"}, "bad_request"), ({}, "bad_request")])
def test_unlock_refusals_run_nothing(run, args, code):
    out = call("mac_unlock", args)
    assert run.calls == [] and out["ok"] is False and out["error"]["code"] == code


def test_unlock_is_refused_for_the_local_mac(run, home):
    home({"macs": {"agent-hub": {"host": "local", "unlock": True}}})
    out = call("mac_unlock", {"mac": "agent-hub"})
    assert run.calls == [] and out["error"]["code"] == "not_allowed"


def test_unlock_timeout_is_not_retried(run):
    run.exc = subprocess.TimeoutExpired("ssh", 25)
    out = call("mac_unlock", {"mac": "agent-mac"})
    assert len(run.calls) == 1 and out["result"] == "transport_timeout" and out["success"] is False
