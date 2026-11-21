"""A misconfigured contract harness must stop its child before application code runs."""

import os
import runpy
import subprocess
import sys
from pathlib import Path
from types import ModuleType, SimpleNamespace

import pytest


@pytest.mark.parametrize(("platform", "endpoints"), [
    ("slack", {}),
    ("discord", {}),
    ("discord", {"HERMES_STANDIN_DISCORD_API": "http://127.0.0.1:1234/api/v10"}),
    ("slack", {"HERMES_STANDIN_SLACK_API": "https://example.invalid/api"}),
])
def test_missing_local_standin_prevents_client_construction(platform, endpoints):
    shim = Path(__file__).with_name(f"{platform}_shim")
    env = {k: v for k, v in os.environ.items() if not k.startswith("HERMES_STANDIN_")}
    # The sentinel stands in for application startup. No SDK or socket is needed.
    child = subprocess.run(
        [sys.executable, "-c", "print('client factory reached')"],
        env={**env, **endpoints, "PYTHONPATH": str(shim)}, capture_output=True, text=True,
        timeout=10, check=False,
    )
    assert child.returncode != 0 and "loopback stand-in" in child.stderr
    assert "client factory reached" not in child.stdout


def test_valid_standins_redirect_sdk_destinations(monkeypatch):
    # Both SDKs are fakes. Loading the shims exercises the same startup code
    # that the real contract harness installs on its child's PYTHONPATH.
    class WebClient:
        def __init__(self, *, base_url="https://slack.com/api/"):
            self.base_url = base_url

    class Session:
        async def _request(self, method, url, **kwargs):
            return url

    modules = {}
    for name in ("slack_sdk", "slack_sdk.web", "slack_sdk.web.base_client",
                 "slack_sdk.web.async_base_client", "aiohttp", "discord",
                 "discord.gateway", "discord.http", "yarl"):
        modules[name] = ModuleType(name)
        monkeypatch.setitem(sys.modules, name, modules[name])
        parent, _, child = name.rpartition(".")
        if parent:
            setattr(modules[parent], child, modules[name])
    modules["slack_sdk.web.base_client"].BaseClient = WebClient
    modules["slack_sdk.web.async_base_client"].AsyncBaseClient = WebClient
    modules["aiohttp"].ClientSession = Session
    modules["discord.http"].Route = SimpleNamespace()
    modules["discord.gateway"].DiscordWebSocket = SimpleNamespace()
    modules["yarl"].URL = str
    api = "http://127.0.0.1:1234/api/"
    gateway = "ws://127.0.0.1:1234/gw"
    monkeypatch.setenv("HERMES_STANDIN_SLACK_API", api)
    monkeypatch.setenv("HERMES_STANDIN_DISCORD_API", api + "v10")
    monkeypatch.setenv("HERMES_STANDIN_DISCORD_GATEWAY", gateway)
    monkeypatch.setenv("HERMES_STANDIN_DISCORD_SHIM_LOADED", "")
    for platform in ("slack", "discord"):
        runpy.run_path(str(Path(__file__).with_name(f"{platform}_shim") / "sitecustomize.py"))
    assert WebClient().base_url == api
    assert WebClient(base_url="https://example.invalid/api/").base_url == api
    assert modules["discord.http"].Route.BASE == api + "v10"
    assert modules["discord.gateway"].DiscordWebSocket.DEFAULT_GATEWAY == gateway
