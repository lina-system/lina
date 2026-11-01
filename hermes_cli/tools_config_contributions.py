"""Read optional built-in setup declarations after tool discovery."""

from tools.registry_contributions import startup_entries


def model_catalogs() -> dict:
    return {name: catalog for entry in startup_entries()
            for name, catalog in (entry.startup.catalogs or {}).items()}


def configurable_toolsets() -> list:
    return [(entry.toolset, *entry.startup.checklist) for entry in startup_entries()
            if entry.startup.checklist]


def tool_categories() -> dict:
    return {entry.toolset: entry.startup.category for entry in startup_entries() if entry.startup.category}


def default_off_toolsets() -> set[str]:
    return {entry.toolset for entry in startup_entries() if entry.startup.default_off}
