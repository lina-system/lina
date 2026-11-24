"""tui_gateway test fixtures.

Several files here import ``tui_gateway.server`` inside a ``patch.dict("sys.modules", {"hermes_constants":
MagicMock(...)})`` window so the module binds a fixed home. The server's import graph reaches
``agent.process_bootstrap`` → ``hermes_bootstrap``, which is process boot: PM dependency activation reads
the real install root through ``hermes_constants`` and exits the process when that is a MagicMock.
Importing it once here, before any window opens, keeps boot out of the mocked import.
"""

import pytest

import hermes_bootstrap


@pytest.fixture
def real_build_revision(monkeypatch: pytest.MonkeyPatch) -> str:
    """Opt in to real install provenance for native-host startup tests."""
    from hermes_cli import version_info

    # The shared test conftest preloads a zero SHA to avoid Git calls.
    monkeypatch.setattr(version_info, "_cached_version_info", None)
    revision = version_info.get_version_info().commit
    assert revision and revision != "0" * 40
    return revision
