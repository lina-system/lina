"""Generation provider downloads reject internal and redirected metadata URLs."""

import importlib

import pytest

from tests.agent import test_remote_fetch_ssrf_guard as shared
listener = shared.listener


SITES = {
    "image_gen/openai::_load_image_bytes": lambda u, mp, tmp: importlib.import_module(
        "plugins.image_gen.openai")._load_image_bytes(u),
    "image_gen/openai-codex::_remote_image_to_data_url": lambda u, mp, tmp: importlib.import_module(
        "plugins.image_gen.openai-codex")._remote_image_to_data_url(u),
}


@pytest.mark.parametrize("site", list(SITES))
def test_provider_fetch_refuses_internal_targets(site, listener, monkeypatch, tmp_path):
    monkeypatch.setattr(shared, "SITES", SITES)
    shared.test_remote_fetch_sites_refuse_internal_targets_before_connect(site, listener, monkeypatch, tmp_path)
