"""``install``'s LINA Helper step (lina#45): what it runs on a Mac and how it reports missing permissions.

The platform is passed as data and the process runner is faked, so the macOS branch is exercised on any host.
"""

from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

from hermes_cli import helper_install as hi


class Runs:
    def __init__(self, permissions=None, fail=()):
        self.calls, self.permissions, self.fail = [], permissions, set(fail)

    def __call__(self, argv, *, cwd=None, capture=False):
        self.calls.append(argv)
        if argv[0] == hi.CTL:
            if self.permissions is None:
                return subprocess.CompletedProcess(argv, 1, "", "no helper")
            return subprocess.CompletedProcess(argv, 0, json.dumps({"v": 1, "ok": True, "result": self.permissions}), "")
        code = 1 if argv[0] in self.fail or (len(argv) > 2 and argv[2] in self.fail) else 0
        return subprocess.CompletedProcess(argv, code, "", "")


@pytest.fixture
def mac(monkeypatch, tmp_path):
    monkeypatch.setattr(hi, "APP", str(tmp_path / "Applications" / "LINA Helper.app"))
    monkeypatch.setattr("hermes_platform.resolver.locate_command",
                        lambda name, *a, **kw: type("R", (), {"command": ["/usr/bin/" + name]})())
    lines = []
    return lines


def test_other_systems_skip_without_running_anything():
    runs, lines = Runs(), []
    assert hi.install_helper(platform="linux", run=runs, out=lines.append) == 0
    assert runs.calls == [] and lines == ["LINA Helper: skipped (it runs on macOS only)."]


def test_without_an_app_builds_ad_hoc_installs_in_user_mode_and_lists_missing_grants(mac):
    runs = Runs(permissions={"accessibility": True, "screen_recording": False})
    assert hi.install_helper(platform="darwin", run=runs, out=mac.append) == 0
    assert runs.calls[0] == ["make", "-C", str(hi.HELPER_DIR), "app"]
    installer = runs.calls[1]
    assert installer[:3] == [hi.SYSTEM_PYTHON, str(hi.HELPER_DIR / "tools" / "install.py"), "install"]
    assert installer[3:] == ["--mode", "user", "--app", str(hi.BUILT_APP), "--allow-ad-hoc-for-lab"]
    assert runs.calls[2][:2] == [hi.CTL, "permissions"]
    text = "\n".join(mac)
    assert "Screen & System Audio Recording" in text and "Privacy & Security" in text
    assert "- Accessibility:" not in text  # granted ones are not listed


def test_signed_app_upgrade_keeps_the_grant_check(monkeypatch, tmp_path):
    (tmp_path / "installed.app").mkdir()
    monkeypatch.setattr(hi, "APP", str(tmp_path / "installed.app"))
    runs, lines = Runs(permissions={"accessibility": True, "screen_recording": True}), []
    assert hi.install_helper(str(tmp_path / "LINA Helper.app"), platform="darwin", run=runs, out=lines.append) == 0
    assert runs.calls[0][2:] == ["upgrade", "--mode", "user", "--app", str(tmp_path / "LINA Helper.app")]
    assert lines[-1] == "LINA Helper has every permission it needs."


def test_ad_hoc_upgrade_skips_the_grant_comparison():
    step = hi.plan(None, None, installed=True)
    assert step.installer_argv()[-2:] == ["--allow-ad-hoc-for-lab", "--skip-grant-check"]
    assert step.notes and "ad-hoc" in step.notes[0]


def test_first_signed_install_needs_the_team(tmp_path):
    with pytest.raises(hi.InstallStepError, match="--team"):
        hi.plan(str(tmp_path / "a.app"), None, installed=False)
    assert hi.plan(str(tmp_path / "a.app"), "ABCDE12345", installed=False).installer_argv()[-2:] == ["--team", "ABCDE12345"]


def test_failures_and_a_silent_helper_are_said_plainly(mac):
    runs = Runs(fail={"make"})
    assert hi.install_helper(platform="darwin", run=runs, out=mac.append) == 1
    assert mac[-1].startswith("LINA Helper: the LINA Helper build failed") and len(runs.calls) == 1
    lines = []
    assert hi.install_helper(platform="darwin", run=Runs(permissions=None), out=lines.append) == 0
    assert "did not answer yet" in lines[-1]


def test_unknown_permission_keys_are_reported_not_dropped():
    assert hi.missing_permissions({"full_disk_access": False, "accessibility": True, "next_alert": {}}) == [
        "- Full Disk Access: open System Settings > Privacy & Security > Full Disk Access, then switch on LINA Helper "
        "(click + and pick /Applications/LINA Helper.app if it is not listed). This lets agents its part of the "
        "helper's work."]


def test_installer_and_built_app_paths_exist_in_the_checkout():
    assert (hi.HELPER_DIR / "tools" / "install.py").is_file()
    assert Path(hi.HELPER_DIR / "Makefile").is_file()
