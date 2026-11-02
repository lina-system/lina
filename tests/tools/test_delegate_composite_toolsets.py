"""Tests for composite toolset expansion in delegate_task intersection."""

import unittest

from tools.delegate_tool import _expand_parent_toolsets, _strip_blocked_tools
from tools.delegate_tool_toolsets import _resolve_child_toolsets
from tools.registry import ToolRegistry
from tools.registry_contributions import StartupContribution


class TestExpandParentToolsets(unittest.TestCase):
    """Verify _expand_parent_toolsets recognises individual toolsets within composites."""

    def test_composite_hermes_cli_expands_web(self):
        """hermes-cli includes web_search/web_extract → 'web' should be in expansion."""
        expanded = _expand_parent_toolsets({"hermes-cli"})
        self.assertIn("web", expanded)
        self.assertIn("terminal", expanded)
        self.assertIn("browser", expanded)
        # Original composite is preserved
        self.assertIn("hermes-cli", expanded)



    def test_included_toolsets_of_composite_parent_are_grantable(self):
        """A composite parent holds its ``includes`` too (#111700): ``debugging`` = terminal/process_manage +
        includes web/file, so a child may request ``web``/``file`` — but never a toolset the parent lacks."""
        expanded = _expand_parent_toolsets({"debugging"})
        self.assertTrue({"debugging", "terminal", "web", "file"} <= expanded)
        self.assertNotIn("browser", expanded)
        self.assertNotIn("hermes-cli", expanded)

    def test_composites_with_allowed_included_tools_are_not_stripped(self):
        toolsets = ["safe", "hermes-gateway", "hermes-cli", "delegation", "kanban"]

        self.assertEqual(
            _strip_blocked_tools(toolsets),
            ["safe", "hermes-gateway", "hermes-cli"],
        )


def _dummy_handler(args, **kwargs):
    return "{}"


def _make_schema(name: str):
    return {"name": name, "description": "startup-only test tool", "parameters": {"type": "object", "properties": {}}}


def _register_startup_only_toolset(monkeypatch, *, toolset: str = "poster_gen", tool: str = "poster_generate"):
    """A toolset with no ``TOOLSETS`` entry: it exists only through a startup declaration on a core bundle
    (the ``image_gen`` shape), so it is installed for every ``hermes-cli`` parent without naming itself statically."""
    reg = ToolRegistry()
    reg.register(
        name=tool, toolset=toolset, schema=_make_schema(tool), handler=_dummy_handler,
        startup=StartupContribution(bundles=("core",), description="Startup-only creative tools"),
    )
    monkeypatch.setattr("tools.registry.registry", reg)
    return toolset


def test_startup_only_toolset_is_grantable_from_composite_parent(monkeypatch):
    """Regression for #75: the parent's ``hermes-cli`` bundle holds ``image_gen``-style startup tools, so a child
    asking for that toolset by name must keep it instead of being dropped as an unknown name."""
    toolset = _register_startup_only_toolset(monkeypatch)

    assert toolset in _expand_parent_toolsets({"hermes-cli"})


def test_delegation_with_explicit_startup_only_toolset_keeps_it(monkeypatch):
    """The child of a composite parent keeps an explicitly requested startup-only toolset."""
    toolset = _register_startup_only_toolset(monkeypatch)
    parent = type("_Parent", (), {"enabled_toolsets": ["hermes-cli"], "disabled_toolsets": []})()

    enabled, _disabled = _resolve_child_toolsets(parent, [toolset], "leaf")

    assert enabled == [toolset]


def test_startup_only_toolset_absent_from_parent_is_not_granted(monkeypatch):
    """Expansion stays bounded by the parent's tools: a startup-only toolset the parent lacks is never granted."""
    toolset = _register_startup_only_toolset(monkeypatch)

    assert toolset not in _expand_parent_toolsets({"web"})


if __name__ == "__main__":
    unittest.main()
