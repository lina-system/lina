"""Hermes computer-use provider ``lina-helper-cua``: Hermes' cua backend, driven through LINA Helper.

Screen control on every Mac goes through one path: Hermes ``computer_use`` -> the bundled cua-driver's
MCP proxy -> LINA Helper's audit gate -> the cua-driver daemon the helper runs with its own grants.
This provider is the upstream ``CuaDriverBackend`` with only the launch changed:

- MCP (and Hermes' CLI fallback) run ``clients/hermes-cua-driver``, which execs the bundled driver
  against the helper's gate on this Mac, or over ssh on another Mac.
- The target Mac is per Hermes session, chosen with the plugin tool ``mac_use`` (default ``hub``).
- The cua permission mode is always ``standard``. Every bot runs ``approvals.mode: off``, which Hermes maps
  to ``unrestricted``; for that the built-in backend starts its own daemon through CuaDriver.app, outside
  the helper. The helper's policy on each Mac is the boundary instead.
- Before input the backend takes the Mac's input lease (``cua.lease``, 5 min, renewed while working). If
  another bot holds it, the action is refused with "<holder> is using this Mac". Reads need no lease. The
  helper's gate enforces the lease too, for clients that skip this step.

Hermes core is unchanged. Selected by directory name: ``computer_use.backend: lina-helper-cua``
(``lina-helper`` is the standalone tools plugin in clients/hermes).
"""
from __future__ import annotations

import os
import sys
import time

from tools.computer_use.backend import ComputerUseBackend, ComputerUseProvider

CLIENTS = os.path.dirname(os.path.dirname(os.path.realpath(__file__)))
WRAPPER = os.path.join(CLIENTS, 'hermes-cua-driver')
LEASE_TTL = 300
LEASE_RENEW_AFTER = 120  # seconds since the last grant before input renews it
INPUT_METHODS = ('click', 'drag', 'scroll', 'type_text', 'key', 'set_value', 'launch_app', 'bring_to_front')


def plugin_core():
    """clients/hermes (the ``lina-helper`` tools plugin) as a package: Mac registry, targets, helper calls."""
    import importlib.util
    name = '_lina_helper_plugin'
    if name + '.core' in sys.modules:
        return sys.modules[name + '.core']
    pkg_dir = os.path.join(CLIENTS, 'hermes')
    spec = importlib.util.spec_from_file_location(name, os.path.join(pkg_dir, '__init__.py'),
                                                  submodule_search_locations=[pkg_dir])
    pkg = importlib.util.module_from_spec(spec)
    sys.modules[name] = pkg
    spec.loader.exec_module(pkg)
    return sys.modules[name + '.core']


class HelperEndpoint:
    """Stands in for upstream's private daemon: tells the cua session which command to launch.

    Upstream ``_CuaDriverSession`` asks an embedded daemon for ``proxy_invocation()``, ``child_env()`` and
    ``socket_path``. This answers with the wrapper aimed at the session's Mac.
    """

    socket_path = ''  # not used: the wrapper drops caller sockets and picks the gate itself

    def __init__(self, host: str, holder: str = '', base_env=None):
        self.host, self.holder = host, holder
        self._base_env = base_env

    def start(self) -> None:
        pass

    def stop(self) -> None:
        pass

    def proxy_invocation(self):
        return WRAPPER, ['mcp']

    def child_env(self) -> dict:
        if self._base_env is not None:
            env = dict(self._base_env)
        else:
            from tools.computer_use.cua_backend import cua_driver_child_env
            env = cua_driver_child_env()
        env.pop('LINA_HELPER_CUA_HOST', None)
        if self.host != 'local':
            env['LINA_HELPER_CUA_HOST'] = self.host
        if self.holder:
            env['LINA_HELPER_CUA_HOLDER'] = self.holder  # CLI fallback calls carry the lease identity
        return env


def make_backend_class():
    from tools.computer_use.backend import ActionResult
    from tools.computer_use.cua_backend import CuaDriverBackend

    class LinaHelperBackend(CuaDriverBackend):
        """Upstream CuaDriverBackend; launched through the helper, leased before input."""

        def __init__(self, session_id: str = '', core=None) -> None:
            super().__init__(permission_mode='standard')
            self._core = core or plugin_core()
            self.mac = self._core.targets.get(session_id)
            self.host = self._core.resolve_host(self.mac)
            self._embedded_daemon = HelperEndpoint(self.host, holder=self._session_id)
            self._session._embedded_daemon = self._embedded_daemon
            self._lease_at = None

        def start(self) -> None:
            # Upstream start() checks the driver contract via HERMES_CUA_DRIVER_CMD (else asks PM to install its
            # pinned driver). Point it at the wrapper for the duration of the check.
            prev = os.environ.get('HERMES_CUA_DRIVER_CMD')
            os.environ['HERMES_CUA_DRIVER_CMD'] = WRAPPER
            try:
                super().start()
            finally:
                if prev is None:
                    os.environ.pop('HERMES_CUA_DRIVER_CMD', None)
                else:
                    os.environ['HERMES_CUA_DRIVER_CMD'] = prev

        def stop(self) -> None:
            if self._lease_at is not None:
                try:
                    self._core.helper_action(self.mac, 'cua.release', {'holder': self._session_id})
                except Exception:
                    pass
                self._lease_at = None
            super().stop()

        def ensure_lease(self, action: str):
            now = time.monotonic()
            if self._lease_at is not None and now - self._lease_at < LEASE_RENEW_AFTER:
                return None
            value = self._core.helper_action(self.mac, 'cua.lease', {'holder': self._session_id, 'ttl_s': LEASE_TTL})
            if value.get('ok'):
                self._lease_at = now
                return None
            err = value.get('error') or {}
            busy = err.get('code') == 'busy'
            return ActionResult(ok=False, action=action, code='mac_busy' if busy else 'lease_failed',
                                message='%s: %s' % (self.mac, err.get('detail') or err.get('code') or 'lease failed'))

    def guard(name):
        upstream = getattr(CuaDriverBackend, name)

        def method(self, *args, **kwargs):
            refusal = self.ensure_lease(name)
            return refusal if refusal is not None else upstream(self, *args, **kwargs)
        method.__name__, method.__doc__ = name, upstream.__doc__
        return method

    for name in INPUT_METHODS:
        if hasattr(CuaDriverBackend, name):
            setattr(LinaHelperBackend, name, guard(name))
    return LinaHelperBackend


_backend_class = None


class LinaHelperCuaProvider(ComputerUseProvider):
    name = 'lina-helper-cua'
    display_name = 'cua-driver through LINA Helper (per-Mac grants, lease, audit)'

    def create_backend(self, *, permission_mode: str) -> ComputerUseBackend:
        global _backend_class
        del permission_mode  # always standard; see the module docstring
        if _backend_class is None:
            _backend_class = make_backend_class()
        core = plugin_core()
        return _backend_class(session_id=core.targets.current_session_id(), core=core)

    def is_available(self) -> bool:
        return sys.platform == 'darwin' and os.access(WRAPPER, os.X_OK)


def register(ctx):
    ctx.register_computer_use_provider(LinaHelperCuaProvider())
