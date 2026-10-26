"""Hermes computer-use provider ``lina-helper-cua``: Hermes' cua backend through LINA Helper.

Same backend as the built-in ``cua`` provider, with two differences:

- The driver command is ``clients/hermes-cua-driver`` (unless HERMES_CUA_DRIVER_CMD is already set), which
  runs the bundled cua-driver's MCP proxy against LINA Helper's audit gate. Every call then runs with the
  helper's grants, under the helper's per-Mac policy, and leaves one line in the helper's audit log.
- The cua permission mode is always ``standard``. Hermes maps an approval bypass (``approvals.mode: off``,
  ``--yolo``) to ``unrestricted``, which makes the built-in backend start its own private daemon through
  CuaDriver.app, outside the helper. Here the helper's policy profile is the boundary instead, so the
  bypass does not change where calls go.

Select it per profile with ``computer_use.backend: lina-helper-cua``. Install: symlink this directory to
``$HERMES_HOME/plugins/lina-helper-cua`` (see clients/hermes-computer-use/README.md).
"""
from __future__ import annotations

import os
import sys

from tools.computer_use.backend import ComputerUseBackend, ComputerUseProvider

_WRAPPER = os.path.join(os.path.dirname(os.path.dirname(os.path.realpath(__file__))), 'hermes-cua-driver')


class LinaHelperCuaProvider(ComputerUseProvider):
    name = 'lina-helper-cua'
    display_name = 'cua-driver through LINA Helper (audited)'

    def create_backend(self, *, permission_mode: str) -> ComputerUseBackend:
        os.environ.setdefault('HERMES_CUA_DRIVER_CMD', _WRAPPER)
        from tools.computer_use.cua_backend import CuaDriverBackend
        return CuaDriverBackend(permission_mode='standard')

    def is_available(self) -> bool:
        return sys.platform == 'darwin' and os.access(os.environ.get('HERMES_CUA_DRIVER_CMD') or _WRAPPER, os.X_OK)


def register(ctx):
    ctx.register_computer_use_provider(LinaHelperCuaProvider())
