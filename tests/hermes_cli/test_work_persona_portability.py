"""Profile portability for the optional work persona."""

from pathlib import Path
from typing import Final

import pytest

from hermes_cli.profiles import create_profile
from hermes_cli.profiles_archive import export_profile, import_profile

WORK_RULES: Final = b"## Work rules\r\n- Review changes.  \r\n"


@pytest.fixture
def install(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    root = tmp_path / "install"
    root.mkdir()
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("LINA_HOME", str(root))
    monkeypatch.setenv("HERMES_HOME", str(root))
    return root


@pytest.mark.parametrize("clone_all", [False, True], ids=["config", "full"])
def test_clone_copies_work_persona(install: Path, clone_all: bool) -> None:
    (install / "SOUL.work.md").write_bytes(WORK_RULES)

    clone = create_profile("editor", clone_from="default", clone_all=clone_all, no_alias=True)

    assert (clone / "SOUL.work.md").read_bytes() == WORK_RULES


@pytest.mark.parametrize("source", ["default", "reviewer"])
def test_archive_round_trip_preserves_work_persona(install: Path, source: str) -> None:
    home = install if source == "default" else install / "profiles" / source
    home.mkdir(parents=True, exist_ok=True)
    (home / "SOUL.md").write_text("Synthetic profile identity.", encoding="utf-8")
    (home / "SOUL.work.md").write_bytes(WORK_RULES)

    archive = export_profile(source, str(install.parent / "profile.tar.gz"))
    restored = import_profile(str(archive), "operator")

    assert (restored / "SOUL.work.md").read_bytes() == WORK_RULES
