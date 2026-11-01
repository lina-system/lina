"""Managed image partner labels follow generation routing."""

import pytest

from hermes_cli import nous_subscription as ns
from tools.image_generation_catalog import DEFAULT_MODEL as FAL_DEFAULT_MODEL

@pytest.mark.parametrize(
    "image_cfg, partner",
    [
        ({"provider": "nous", "model": "krea-2-medium"}, "Krea"),
        ({"model": FAL_DEFAULT_MODEL}, "FAL"),
        ({"provider": "nous", "model": "openai/gpt-image-2"}, "Nous Portal"),
        ({"model": "openai/gpt-image-2"}, "FAL"),
        ({"use_gateway": True, "model": "openai/gpt-image-2"}, "FAL"),  # managed-model routing ignores legacy use_gateway
        ({"provider": "openai", "model": "gpt-image-2"}, None),
    ],
)
def test_managed_image_partner_follows_the_stored_model(image_cfg, partner):
    """The partner is the gateway the runtime dispatcher routes to (tools.image_generation_managed.
    managed_route): the stored model decides under the managed pick, Portal ids only with an
    explicit ``nous``; a direct vendor owns its model id."""
    assert ns.managed_image_partner({"image_gen": image_cfg}) == partner
