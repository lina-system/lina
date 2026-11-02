"""The desktop's offline slash block-list is a dump of COMMAND_REGISTRY, never hand-authored."""


from hermes_cli.commands import COMMAND_REGISTRY, desktop_surface_registry, resolve_command


def test_desktop_surface_registry_covers_every_alias_with_its_canonical_value():
    registry = desktop_surface_registry()
    for cmd in COMMAND_REGISTRY:
        for key in (cmd.name, *cmd.aliases):
            assert registry.get(f"/{key}") == (cmd.desktop or None), key
            assert f"/{key}" in registry, key
    for key in registry:
        assert resolve_command(key) is not None, key
