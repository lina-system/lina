# SPDX-License-Identifier: AGPL-3.0-only
"""LINA release identity.

LINA is a fork of Hermes Agent. ``LINA_VERSION`` is the one place the displayed (SemVer) version lives;
``pyproject.toml`` carries the same version in PEP 440 form. ``LINA_UPSTREAM_COMMIT`` is the Hermes
``upstream/main`` commit this line is based on; update it whenever upstream is merged into ``dev``
(see docs/forking.md and docs/versioning.md). No imports: the --version fast path (hermes_cli/_startup_fast.py) loads this module.
"""

LINA_VERSION = "0.0.1-alpha.20261102.1"
LINA_UPSTREAM_COMMIT = "c7c7e0fa92"


def lina_version_line() -> str:
    return f"LINA {LINA_VERSION} (Hermes upstream {LINA_UPSTREAM_COMMIT})"
