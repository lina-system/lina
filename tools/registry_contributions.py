"""Startup metadata supplied by built-in tools, independent of profile overlays.

Discovery precedes session construction. These declarations describe installed
capabilities, never availability or credentials; consumers read profile state
only when invoking a callback. Runtime tool registrations cannot change them.
"""

from dataclasses import dataclass
from threading import RLock
from typing import Callable
from weakref import WeakKeyDictionary


@dataclass(frozen=True, slots=True)
class StartupContribution:
    bundles: tuple[str, ...] = ()
    description: str = ""
    checklist: tuple[str, str] | None = None
    category: dict | None = None
    default_off: bool = False
    catalogs: dict | None = None
    managed_partner: Callable | None = None
    rpc_installer: Callable | None = None
    distributions: dict | None = None


_lock = RLock()
_discovered = False
_discovering = False
_snapshots = WeakKeyDictionary()


def startup_entries(*, discover: bool = True):
    """Global built-in declarations only; session/profile overrides are excluded."""
    from tools.registry import discover_builtin_tools, registry

    global _discovered, _discovering
    with _lock:
        if registry in _snapshots:
            return _snapshots[registry]
        if discover and not _discovered and not _discovering:
            _discovering = True
            try:
                discover_builtin_tools()
                _discovered = True
            finally:
                _discovering = False
        with registry._lock:
            entries = tuple(entry for entry in registry._tools.values() if entry.startup is not None)
        if discover and not _discovering:
            _snapshots[registry] = entries
        return entries


def startup_frozen(registry) -> bool:
    return registry in _snapshots


def bundle_tools(bundle: str, *, discover: bool = True) -> list[str]:
    return [entry.name for entry in startup_entries(discover=discover) if bundle in entry.startup.bundles]


def toolset_contribution(name: str) -> StartupContribution | None:
    return next((entry.startup for entry in startup_entries() if entry.toolset == name), None)


class CoreTools(list):
    """Static core names plus installed startup contributions, without mutating either.

    Existing core-membership consumers iterate this list at session construction.
    Import-time bundle definitions use the static portion; resolution merges the
    same contributions after discovery has completed.
    """

    def __iter__(self):
        return iter(dict.fromkeys([*super().__iter__(), *bundle_tools("core", discover=False)]))

    def __contains__(self, name):
        return super().__contains__(name) or name in bundle_tools("core", discover=False)

    def __add__(self, other):
        return [*self, *other]
