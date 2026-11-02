"""Image provider credentials honor the active profile."""

from tests.plugins import test_secret_scope_plugin_families as _families

multiplex_scope = _families.multiplex_scope

# ---------------------------------------------------------------------------
# Family B — image_gen plugins
# ---------------------------------------------------------------------------

class TestImageGenFamily:
    def test_deepinfra_scoped_key_wins(self, multiplex_scope, monkeypatch):
        monkeypatch.delenv("DEEPINFRA_API_KEY", raising=False)
        multiplex_scope({"DEEPINFRA_API_KEY": "scoped-key"})

        from plugins.image_gen.deepinfra import DeepInfraImageGenProvider

        assert DeepInfraImageGenProvider().is_available() is True

    def test_deepinfra_scoped_miss_does_not_borrow_environ(
        self, multiplex_scope, monkeypatch
    ):
        monkeypatch.setenv("DEEPINFRA_API_KEY", "env-other-profile")
        multiplex_scope({})

        from plugins.image_gen.deepinfra import DeepInfraImageGenProvider

        assert DeepInfraImageGenProvider().is_available() is False
