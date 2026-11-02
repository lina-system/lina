"""Managed image models are exposed through the GUI catalog."""

from types import SimpleNamespace

from hermes_cli.nous_account import NousPortalAccountInfo
from tests.hermes_cli.test_tools_config import _managed_image_row

def test_gui_model_catalog_for_the_managed_row_spans_every_managed_gateway(monkeypatch):
    from hermes_cli import tools_config
    from hermes_cli.web_routers.tools import _resolve_toolset_model_plugin, _toolset_model_catalog
    from plugins.image_gen.krea import KREA_MODEL_IDS
    from tools.image_generation_catalog import FAL_MODELS

    paid = NousPortalAccountInfo(logged_in=True, source="jwt", fresh=False, paid_service_access=True)
    monkeypatch.setattr(
        tools_config, "get_nous_subscription_features",
        lambda *args, **kwargs: SimpleNamespace(features={}, account_info=paid))
    plugin = _resolve_toolset_model_plugin("image_gen", _managed_image_row())
    catalog, default_model = _toolset_model_catalog("image_gen", plugin, {})

    assert default_model in catalog and default_model in FAL_MODELS
    assert KREA_MODEL_IDS <= set(catalog)
    assert not any(mid.startswith("fal-ai/krea/") for mid in catalog), "Krea 2 must appear once, natively"
