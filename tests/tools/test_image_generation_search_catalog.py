"""Deferred image catalog preserves generation capabilities."""

class TestImageCatalog:
    def test_image_generate_catalog_line_names_edit_refs_and_configured_model(self):
        """The deferred image_generate line must say it edits with references on the
        configured model, or agents bypass it with direct provider scripts."""
        from unittest.mock import patch

        import tools.image_generation_tool as ig
        from tools.tool_search_catalog import _short_desc

        model = next(m for m, meta in ig.FAL_MODELS.items()
                     if meta.get("edit_endpoint") and int(meta.get("max_reference_images") or 0) > 1)
        with patch.object(ig, "_resolve_fal_model", return_value=(model, ig.FAL_MODELS[model])), \
                patch.object(ig, "_read_configured_image_provider", return_value=None):
            line = _short_desc(ig._build_dynamic_image_schema()["description"])
        assert not line.endswith("…")
        for phrase in ("edit", "reference image", "configured model"):
            assert phrase in line
