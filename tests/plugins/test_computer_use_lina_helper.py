"""The built-in ``lina_helper`` computer-use provider on the real upstream CuaDriverBackend.

Upstream input methods are replaced before the subclass is built (the lease guard wraps what it finds), so no
driver runs; the lease, the endpoint and the forced ``standard`` mode are the provider's own behaviour.
"""

from __future__ import annotations

import os
import types

import pytest

import hermes_cli.config as hc
import hermes_yaml as yaml
from tools.computer_use.backend import ActionResult


class Helper:
    def __init__(self, lease=None):
        self.targets = types.SimpleNamespace(get=lambda sid: "agent-mac" if sid == "remote" else "agent-hub")
        self.calls = []
        self.lease = lease or {"ok": True, "result": {}}

    def resolve(self, mac):
        return types.SimpleNamespace(host="box-1" if mac == "agent-mac" else "local")

    def helper_action(self, mac, action, args):
        self.calls.append((mac, action, args))
        return self.lease if action == "cua.lease" else {"ok": True, "result": {}}


@pytest.fixture
def provider(monkeypatch):
    from plugins.computer_use import load_computer_use_provider
    from tools.computer_use.cua_backend import CuaDriverBackend
    upstream = []
    for name in ("click", "type_text"):
        monkeypatch.setattr(CuaDriverBackend, name,
                            lambda self, *a, _n=name, **kw: upstream.append(_n) or ActionResult(ok=True, action=_n))
    monkeypatch.setattr(CuaDriverBackend, "start", lambda self: upstream.append(("start", os.environ.get("HERMES_CUA_DRIVER_CMD"))))
    monkeypatch.setattr(CuaDriverBackend, "stop", lambda self: upstream.append("stop"))
    prov = load_computer_use_provider("lina_helper")
    mod = __import__(type(prov).__module__, fromlist=["make_backend_class"])
    return types.SimpleNamespace(prov=prov, mod=mod, cls=mod.make_backend_class(), upstream=upstream)


def test_selected_by_computer_use_backend(monkeypatch):
    from plugins.computer_use import get_active_provider
    with open(os.path.join(os.environ["HERMES_HOME"], "config.yaml"), "w") as f:
        f.write(yaml.safe_dump({"computer_use": {"backend": "lina_helper"}}))
    hc._LOAD_CONFIG_CACHE.clear()
    try:
        assert get_active_provider().name == "lina_helper"
    finally:
        hc._LOAD_CONFIG_CACHE.clear()


def test_endpoint_points_the_session_at_the_wrapper_and_the_mac(provider):
    local = provider.cls(session_id="s", helper=Helper())
    assert local.permission_mode == "standard"
    assert local._session._embedded_daemon is local._embedded_daemon
    assert local._embedded_daemon.proxy_invocation() == (provider.mod.WRAPPER, ["mcp"])
    assert os.access(provider.mod.WRAPPER, os.X_OK)
    assert "LINA_HELPER_CUA_HOST" not in local._embedded_daemon.child_env()
    remote = provider.cls(session_id="remote", helper=Helper())
    env = remote._embedded_daemon.child_env()
    assert remote.mac == "agent-mac" and env["LINA_HELPER_CUA_HOST"] == "box-1"
    assert env["LINA_HELPER_CUA_HOLDER"] == remote._session_id


def test_start_checks_the_contract_against_the_wrapper_and_restores_env(provider, monkeypatch):
    b = provider.cls(session_id="s", helper=Helper())
    monkeypatch.setenv("HERMES_CUA_DRIVER_CMD", "/x")
    b.start()
    assert os.environ["HERMES_CUA_DRIVER_CMD"] == "/x"
    assert provider.upstream[-1] == ("start", provider.mod.WRAPPER)


def test_input_takes_the_lease_once_and_stop_releases_it(provider, monkeypatch):
    helper = Helper()
    b = provider.cls(session_id="s", helper=helper)
    assert b.click(x=1, y=2).ok and b.type_text("hi").ok
    assert helper.calls == [("agent-hub", "cua.lease", {"holder": b._session_id, "ttl_s": 300})]
    monkeypatch.setattr(provider.mod.time, "monotonic", lambda: 10 ** 9)
    b.click(x=1, y=2)
    assert len(helper.calls) == 2
    b.stop()
    assert helper.calls[-1][1] == "cua.release" and provider.upstream[-1] == "stop"


def test_busy_lease_refuses_without_calling_upstream(provider):
    helper = Helper(lease={"ok": False, "error": {"code": "busy", "detail": "hermes-other is using this Mac (200s left)"}})
    r = provider.cls(session_id="s", helper=helper).click(x=1, y=2)
    assert not r.ok and r.code == "mac_busy" and "hermes-other is using this Mac" in r.message
    assert "click" not in provider.upstream


def test_create_backend_forces_standard(provider, monkeypatch):
    from tools.lina_helper import actions
    monkeypatch.setattr(actions, "resolve", lambda name: types.SimpleNamespace(host="local"))
    for mode in ("standard", "bounded", "unrestricted"):
        assert provider.prov.create_backend(permission_mode=mode).permission_mode == "standard"


def test_bundled_driver_recovers_and_cli_fallback_uses_the_helper_endpoint(provider, monkeypatch):
    """An install with only the helper's bundled driver has no PM cua-driver.

    A closed MCP transport must come back through the session endpoint, and the CLI fallback must call that
    same endpoint. Both used to resolve the default driver again and fail once start()'s override was gone.
    """
    import contextlib
    from unittest.mock import AsyncMock, MagicMock, patch

    from anyio import ClosedResourceError
    from tools.computer_use.cua_backend_session import _AsyncBridge

    monkeypatch.delenv("HERMES_CUA_DRIVER_CMD", raising=False)
    monkeypatch.setattr("pm.installed_package", lambda *args, **kwargs: None)
    backend = provider.cls(session_id="remote", helper=Helper())
    session = backend._session
    bridge = _AsyncBridge()
    session._bridge = bridge
    bridge.start()
    initial = MagicMock()
    initial.call_tool = AsyncMock(side_effect=ClosedResourceError())
    session._session = initial
    session._started = True
    spawned = {}
    ready = MagicMock()
    ready.content = []
    ready.isError = False
    ready.structuredContent = None
    live = MagicMock()
    live.initialize = AsyncMock()
    live.list_tools = AsyncMock(return_value=MagicMock(tools=[]))
    live.call_tool = AsyncMock(return_value=ready)

    def spawn(params):
        spawned["command"] = params.command
        spawned["args"] = list(params.args)
        spawned["env"] = dict(params.env)
        client = MagicMock()
        client.__aenter__ = AsyncMock(return_value=(MagicMock(), MagicMock()))
        client.__aexit__ = AsyncMock(return_value=None)
        return client

    try:
        with patch("mcp.client.stdio.stdio_client", side_effect=spawn), \
                patch("mcp.ClientSession") as client_session:
            client_session.return_value.__aenter__ = AsyncMock(return_value=live)
            client_session.return_value.__aexit__ = AsyncMock(return_value=None)
            out = session.call_tool("list_windows", {}, timeout=5)
        assert out.get("isError") is not True
        assert spawned["command"] == provider.mod.WRAPPER
        assert spawned["args"] == ["mcp"]
        assert spawned["env"]["LINA_HELPER_CUA_HOST"] == "box-1"
        assert spawned["env"]["LINA_HELPER_CUA_HOLDER"] == backend._session_id
        proc = MagicMock(stdout="{}", stderr="", returncode=0)
        with patch("subprocess.run", return_value=proc) as run:
            session._call_tool_via_cli("list_windows", {}, timeout=5)
        cmd = run.call_args.args[0]
        env = run.call_args.kwargs["env"]
        assert cmd[0] == provider.mod.WRAPPER
        assert cmd[1:3] == ["call", "list_windows"]
        assert env["LINA_HELPER_CUA_HOST"] == "box-1"
        assert env["LINA_HELPER_CUA_HOLDER"] == backend._session_id
    finally:
        loop, event = session._bridge._loop, session._shutdown_event
        if loop is not None and event is not None and loop.is_running():
            loop.call_soon_threadsafe(event.set)
        future = session._lifecycle_future
        if future is not None:
            with contextlib.suppress(Exception):
                future.result(timeout=5)
        bridge.stop()
