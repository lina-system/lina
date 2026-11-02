"""Strict image and video provider selection contracts."""

from types import SimpleNamespace
from unittest.mock import patch

import pytest

MANAGED = SimpleNamespace(
    nous_user_token="managed-token",
    gateway_origin="https://gateway.nousresearch.com",
)

# ---------------------------------------------------------------------------
# Image generation (FAL)
# ---------------------------------------------------------------------------


class TestImageFalStrictSelection:
    def test_nous_selection_routes_managed_even_with_fal_key(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value="nous"), \
             patch.object(it, "fal_key_is_configured", return_value=True), \
             patch.object(it, "resolve_managed_tool_gateway", return_value=MANAGED) as gw:
            assert it._resolve_managed_fal_gateway() is MANAGED
        gw.assert_called_once_with("fal-queue")

    def test_nous_selection_unentitled_raises_selection_error(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value="nous"), \
             patch.object(it, "fal_key_is_configured", return_value=True), \
             patch.object(it, "resolve_managed_tool_gateway", return_value=None):
            with pytest.raises(ValueError) as exc:
                it._resolve_managed_fal_gateway()
        assert "image_gen is configured to use nous" in str(exc.value)
        assert "hermes tools" in str(exc.value)

    def test_fal_selection_missing_key_errors_without_managed_call(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value="fal"), \
             patch.object(it, "fal_key_is_configured", return_value=False), \
             patch.object(it, "resolve_managed_tool_gateway") as gw:
            with pytest.raises(ValueError) as exc:
                it._resolve_managed_fal_gateway()
        gw.assert_not_called()
        assert "FAL_KEY" in str(exc.value)
        assert "image_gen is configured to use fal" in str(exc.value)
        assert "hermes tools" in str(exc.value)

    def test_fal_selection_with_key_routes_direct(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value="fal"), \
             patch.object(it, "fal_key_is_configured", return_value=True), \
             patch.object(it, "resolve_managed_tool_gateway") as gw:
            assert it._resolve_managed_fal_gateway() is None
        gw.assert_not_called()

    def test_never_configured_autodetect_direct_when_key_present(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value=None), \
             patch.object(it, "fal_key_is_configured", return_value=True):
            assert it._resolve_managed_fal_gateway() is None

    def test_never_configured_autodetect_managed_when_no_key(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value=None), \
             patch.object(it, "fal_key_is_configured", return_value=False), \
             patch.object(it, "resolve_managed_tool_gateway", return_value=MANAGED):
            assert it._resolve_managed_fal_gateway() is MANAGED

    def test_check_fal_api_key_reflects_selection(self):
        from tools import image_generation_tool as it

        with patch.object(it, "read_selection", return_value="fal"), \
             patch.object(it, "fal_key_is_configured", return_value=False), \
             patch.object(it, "resolve_managed_tool_gateway", return_value=MANAGED):
            # Broken vendor selection reports unavailable even though the
            # managed gateway would resolve.
            assert it.check_fal_api_key() is False


# ---------------------------------------------------------------------------
# Video generation (FAL plugin)
# ---------------------------------------------------------------------------


class TestVideoFalStrictSelection:
    def test_nous_selection_routes_managed_even_with_fal_key(self):
        from plugins.video_gen import fal as vf

        with patch("tools.tool_backend_helpers.read_selection", return_value="nous"), \
             patch("tools.tool_backend_helpers.fal_key_is_configured", return_value=True), \
             patch("tools.managed_tool_gateway.resolve_managed_tool_gateway", return_value=MANAGED):
            assert vf._resolve_managed_fal_video_gateway() is MANAGED

    def test_fal_selection_missing_key_errors_without_managed_call(self):
        from plugins.video_gen import fal as vf

        with patch("tools.tool_backend_helpers.read_selection", return_value="fal"), \
             patch("tools.tool_backend_helpers.fal_key_is_configured", return_value=False), \
             patch("tools.managed_tool_gateway.resolve_managed_tool_gateway") as gw:
            with pytest.raises(ValueError) as exc:
                vf._resolve_managed_fal_video_gateway()
        gw.assert_not_called()
        assert "video_gen is configured to use fal" in str(exc.value)
        assert "FAL_KEY" in str(exc.value)

    def test_never_configured_autodetect_unchanged(self):
        from plugins.video_gen import fal as vf

        with patch("tools.tool_backend_helpers.read_selection", return_value=None), \
             patch("tools.tool_backend_helpers.fal_key_is_configured", return_value=True):
            assert vf._resolve_managed_fal_video_gateway() is None
