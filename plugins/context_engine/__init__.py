"""Context engine plugin discovery: bundled ``plugins/context_engine/<name>/`` then user
``$HERMES_HOME/plugins/<name>/`` (bundled wins on collision) → ``ContextEngine``. Separate from the
general plugin system: ``context.engine`` in config.yaml names the active engine (default
``"compressor"``, the built-in ContextCompressor), so a user-installed engine needs no
``plugins.enabled`` entry to be selectable."""

from __future__ import annotations

import logging
from contextlib import contextmanager
from pathlib import Path
from typing import TYPE_CHECKING, List, Optional, Tuple

from plugins import plugin_loader as _loader

if TYPE_CHECKING:
    from agent.context_engine import ContextEngine

logger = logging.getLogger(__name__)

_CONTEXT_ENGINE_PLUGINS_DIR = Path(__file__).parent
# Synthetic parent package for user-installed engines (keeps them out of the bundled namespace).
_USER_NAMESPACE = "_hermes_user_context_engine"
# Engine names a config may still use for a bundled engine. ``lcm`` is LCM-X's pre-rename name; LCM-X itself
# accepts it and warns that the config should say ``lcm-x`` (plugin_identity.identity_migration_notice).
_ENGINE_ALIASES = {"lcm": "lcm-x"}
# The engines this process loaded in-process, per profile home key: a forced plugin discovery (an install or
# enable in a running gateway) unloads every registration, and cached agents never rerun the loader.
_LOADED_ENGINE_DIRS: dict[str, dict[str, Path]] = {}


def _is_context_engine_dir(path: Path) -> bool:
    """Cheap text heuristic: ``__init__.py`` mentions the context engine contract."""
    init_file = path / "__init__.py"
    try:
        source = init_file.read_text(errors="replace", encoding="utf-8-sig")[:8192]
    except OSError:
        return False
    return "register_context_engine" in source or "ContextEngine" in source


def _iter_engine_dirs() -> list[tuple[str, Path]]:
    """``(name, path)`` for bundled then user engines; bundled wins on collisions."""
    dirs = [(child.name, child) for child in _loader.iter_plugin_dirs(_CONTEXT_ENGINE_PLUGINS_DIR)]
    seen = {name for name, _ in dirs}
    user_dir = _loader.user_plugins_dir()
    if user_dir:
        dirs.extend((child.name, child) for child in _loader.iter_plugin_dirs(user_dir)
                    if child.name not in seen and _is_context_engine_dir(child))
    return dirs


def discover_context_engines() -> list[tuple[str, str, bool]]:
    """Return ``[(name, description, is_available), ...]`` for every bundled and user engine. Probing
    registers nothing: only the selected engine's load puts hooks, skills and prompt sections in the home."""
    return [(name, _loader.read_plugin_description(child),
             _loader.probe_availability(lambda c=child: _load_engine_from_dir(c, register=False)))
            for name, child in _iter_engine_dirs()]


def find_engine_dir(name: str) -> Optional[Path]:
    """Resolve an engine name (or a legacy alias of a bundled engine) to its directory, bundled first."""
    name = _ENGINE_ALIASES.get(name, name)
    bundled = _CONTEXT_ENGINE_PLUGINS_DIR / name
    if bundled.is_dir():
        return bundled
    user_dir = _loader.user_plugins_dir()
    user = user_dir / name if user_dir else None
    return user if user and user.is_dir() and _is_context_engine_dir(user) else None


def load_context_engine(name: str) -> Optional[ContextEngine]:
    """Load a ContextEngine instance by name; None if not found or it fails to load."""
    engine_dir = find_engine_dir(name)
    if engine_dir is None:
        logger.debug("Context engine '%s' not found in bundled or user plugins", name)
        return None
    return _loader.load_named(
        name, engine_dir, _load_engine_from_dir, kind="Context engine", noun="engine", logger=logger
    )


def _load_engine_from_dir(engine_dir: Path, register: bool = True) -> Optional[ContextEngine]:
    """Import an engine module and extract its ContextEngine (register(ctx) or subclass). With
    ``register=False`` (an availability probe) ``register(ctx)`` gets a context that keeps only the engine."""
    from agent.context_engine import ContextEngine
    name = engine_dir.name
    is_bundled = engine_dir.parent == _CONTEXT_ENGINE_PLUGINS_DIR
    module_name = f"plugins.context_engine.{name}" if is_bundled else f"{_USER_NAMESPACE}.{name}"
    from hermes_cli.plugin_isolation import user_plugin_host
    host = None if is_bundled else user_plugin_host()
    if host is not None:
        return host.load_instance(engine_dir, module_name=module_name, capture="register_context_engine",
                                  base_ref="agent.context_engine:ContextEngine")
    mod = _loader.load_plugin_module(
        module_name, engine_dir, parents=("plugins", "plugins.context_engine"), logger=logger,
        synthetic_namespace=None if is_bundled else _USER_NAMESPACE)
    if not mod:
        return None
    if not register:
        return _loader.instance_from_module(
            mod, collector=_ProbeCollector(), collected_attr="engine", base_cls=ContextEngine, name=name,
            logger=logger)
    collector = _EngineCollector(engine_name=name, engine_dir=engine_dir)
    with collector.replacing_previous_build():
        engine = _loader.instance_from_module(
            mod, collector=collector, collected_attr="engine", base_cls=ContextEngine, name=name, logger=logger)
    if engine is not None:
        from hermes_constants import hermes_home_key
        _LOADED_ENGINE_DIRS.setdefault(hermes_home_key(), {})[name] = engine_dir
    return engine


def re_register_config_hooks() -> None:
    """After a forced plugin discovery cleared them, register again the hooks, skill and prompt section of
    every engine this process loaded in the active home, by rerunning the engine's own ``register(ctx)``.
    The hooks resolve the agent's engine through the engine's registry, so cached agents get them back;
    the instance this builds is dropped, and the next agent build replaces the group as usual."""
    from hermes_constants import hermes_home_key
    for engine_dir in list(_LOADED_ENGINE_DIRS.get(hermes_home_key(), {}).values()):
        _load_engine_from_dir(engine_dir)


class _ProbeCollector:
    """Plugin context for an availability probe: keeps the engine, drops every other ``register_*`` call."""

    def __init__(self):
        self.engine = None

    def register_context_engine(self, engine):
        self.engine = engine

    def __getattr__(self, attr: str):
        if not attr.startswith("register_"):
            raise AttributeError(attr)
        return lambda *args, **kwargs: None


class _EngineCollector:
    """Plugin context for context engines. ``register_context_engine`` is captured (one engine instance
    per agent build); every other ``register_*`` call goes to a real ``PluginContext`` of the active
    profile home, so an engine's hooks, skills and prompt sections register like a plugin's. They form a
    fallback group keyed by (engine name, source path), the shape memory providers use: each agent build
    replaces the group, so a home holds one set however many agents are built."""

    def __init__(self, engine_name: str = "", engine_dir: Optional[Path] = None):
        self.engine = None
        self._engine_name = engine_name or "context_engine"
        self._engine_dir = engine_dir
        self._source = (self._engine_name, str((engine_dir / "__init__.py").resolve())) if engine_dir else None
        self._context = None

    @contextmanager
    def replacing_previous_build(self):
        """Drop the group the previous build of this engine registered in this home, then hold the
        manager's discovery lock while ``register(ctx)`` builds the new one."""
        manager = self._plugin_context()._manager
        with manager._discovery_lock:
            manager._drop_fallback_hooks(self._source)
            yield

    def register_context_engine(self, engine):
        self.engine = engine

    def register_hook(self, hook_name, callback, *, hold_stream=False):
        context = self._plugin_context()
        with context._manager._discovery_lock:
            return context._manager._register_fallback_hook(
                context, self._source, hook_name, callback, hold_stream=hold_stream)

    def __getattr__(self, attr: str):
        """Forward any other ``register_*`` call to the real ``PluginContext`` and add its handle to the
        group. Other attributes raise, so an engine probing for a host capability sees it absent."""
        if not attr.startswith("register_"):
            raise AttributeError(attr)

        def _forward(*args, **kwargs):
            context = self._plugin_context()
            handle = getattr(context, attr)(*args, **kwargs)
            if handle is not None and self._source is not None:
                context._manager._add_fallback_registration(self._source, handle)
            return handle

        return _forward

    def _plugin_context(self):
        if self._context is None:
            from hermes_cli.plugins import PluginContext, PluginManifest, get_plugin_manager

            # plugin.yaml's name keeps qualified skill names (``hermes-lcm-x:hermes-lcm``) as they were.
            name = (_loader.read_plugin_field(self._engine_dir, "name", self._engine_name)
                    if self._engine_dir else self._engine_name)
            manifest = PluginManifest(name=name, key=f"context_engine/{self._engine_name}")
            self._context = PluginContext(manifest, get_plugin_manager())
        return self._context

    def register_command(self, name: str, handler, description: str = "", args_hint: str = "") -> None:
        clean = (name or "").lower().strip().lstrip("/").replace(" ", "-")
        if not clean:
            logger.warning("Context engine '%s' tried to register a command with an empty name.",
                           self._engine_name)
            return
        conflict = "Context engine '%s' tried to register command '/%s' which %s Skipping."
        try:
            from hermes_cli.commands import resolve_command
            if resolve_command(clean) is not None:
                logger.warning(conflict, self._engine_name, clean, "conflicts with a built-in command.")
                return
        except Exception:
            pass
        try:
            from hermes_cli.plugins import get_plugin_manager
            manager = get_plugin_manager()
            if clean in manager._plugin_commands:
                logger.warning(conflict, self._engine_name, clean, "is already registered by a plugin.")
                return
            manager._plugin_commands[clean] = {
                "handler": handler, "description": description or "Context engine command",
                "plugin": f"context-engine:{self._engine_name}", "args_hint": (args_hint or "").strip()}
            logger.debug("Context engine '%s' registered command: /%s", self._engine_name, clean)
        except Exception as exc:
            logger.debug("Context engine '%s' could not register /%s: %s", self._engine_name, clean, exc)
