"""Client-identity headers the plugin attaches to Honcho requests once the user opts in."""

import sys
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

import plugins.memory.honcho.client as client_mod
from plugins.memory.honcho.client import HonchoClientConfig, get_honcho_client, telemetry_headers


@pytest.fixture(autouse=True)
def _fresh_client_cache():
    client_mod.reset_honcho_client()
    yield
    client_mod.reset_honcho_client()


def test_plugin_header_carries_the_manifest_version(tmp_path, monkeypatch):
    (tmp_path / "plugin.yaml").write_text('name: honcho\nversion: "9.8.7"\n')
    monkeypatch.setattr(client_mod, "__file__", str(tmp_path / "client.py"))

    assert telemetry_headers()["X-Honcho-Plugin"] == "hermes-plugin-honcho/9.8.7"


def test_plugin_header_matches_the_shipped_manifest():
    manifest = Path(client_mod.__file__).with_name("plugin.yaml").read_text()
    version = next(line.split(":", 1)[1].strip() for line in manifest.splitlines() if line.startswith("version:"))

    assert telemetry_headers()["X-Honcho-Plugin"] == f"hermes-plugin-honcho/{version}"


@pytest.mark.parametrize("manifest", [None, "name: honcho\n", "version:\n"])
def test_plugin_header_falls_back_to_unknown(tmp_path, monkeypatch, manifest):
    if manifest is not None:
        (tmp_path / "plugin.yaml").write_text(manifest)
    monkeypatch.setattr(client_mod, "__file__", str(tmp_path / "client.py"))

    assert telemetry_headers()["X-Honcho-Plugin"] == "hermes-plugin-honcho/unknown"


def test_host_header_includes_the_hermes_version(monkeypatch):
    monkeypatch.setattr(client_mod, "_hermes_version", lambda: "0.21.3")

    assert telemetry_headers()["X-Honcho-Host"] == f"hermes/0.21.3 ({sys.platform})"


def test_host_header_omits_an_unresolvable_hermes_version(monkeypatch):
    monkeypatch.setitem(sys.modules, "hermes_cli", None)  # makes `from hermes_cli import ...` raise

    assert client_mod._hermes_version() == ""
    assert telemetry_headers()["X-Honcho-Host"] == f"hermes ({sys.platform})"


def _fake_sdk(monkeypatch) -> MagicMock:
    """Stand-in for `from honcho import Honcho`, so the client build runs without the opt-in SDK extra."""
    honcho_cls = MagicMock(name="Honcho")
    monkeypatch.setitem(sys.modules, "honcho", SimpleNamespace(Honcho=honcho_cls))
    return honcho_cls


def test_default_client_sends_no_attribution_headers(monkeypatch):
    """lina#14: without the telemetry opt-in no request carries X-Honcho-Host / X-Honcho-Plugin."""
    honcho_cls = _fake_sdk(monkeypatch)
    get_honcho_client(HonchoClientConfig(api_key="k", base_url="http://localhost:8000", workspace_id="hermes"))

    headers = honcho_cls.call_args.kwargs.get("default_headers") or {}
    assert "X-Honcho-Host" not in headers and "X-Honcho-Plugin" not in headers


def test_opted_in_client_sends_the_telemetry_headers(monkeypatch):
    honcho_cls = _fake_sdk(monkeypatch)
    get_honcho_client(HonchoClientConfig(api_key="k", base_url="http://localhost:8000", workspace_id="hermes",
                                         telemetry=True))

    assert honcho_cls.call_args.kwargs["default_headers"] == telemetry_headers()


def test_telemetry_opt_in_is_read_from_honcho_json():
    from hermes_constants import get_hermes_home

    assert HonchoClientConfig.from_global_config().telemetry is False
    (get_hermes_home() / "honcho.json").write_text('{"apiKey": "k", "telemetry": true}', encoding="utf-8")
    assert HonchoClientConfig.from_global_config().telemetry is True
