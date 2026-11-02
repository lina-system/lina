"""``install`` step for LINA Helper (``helper/macos``): install or update the app on a Mac, then say plainly
which permissions the person still has to switch on. Other systems skip it.

The app comes from ``--app`` (a signed build, with ``--team``) or, without one, from an ad-hoc build of the
checkout (``make -C helper/macos app``). The helper's own installer does the work in user mode
(``helper/macos/tools/install.py``); it never touches the privacy database, so grants are given by a person in
System Settings.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

HELPER_DIR = Path(__file__).resolve().parents[1] / "helper" / "macos"
APP = "/Applications/LINA Helper.app"
CTL = APP + "/Contents/Helpers/lina-helperctl"
BUILT_APP = HELPER_DIR / "build" / "LINA Helper.app"
SYSTEM_PYTHON = "/usr/bin/python3"  # the installer is standard-library only and runs with the system Python

# Permission key from `lina-helperctl permissions` -> (System Settings pane, what it lets agents do).
PERMISSIONS = {
    "accessibility": ("Accessibility", "read and press things on the screen, type and click"),
    "screen_recording": ("Screen & System Audio Recording", "see the screen (screenshots and computer use)"),
}


@dataclass
class Plan:
    app: Path
    command: str  # install | upgrade
    adhoc: bool
    team: str | None = None
    notes: list[str] = field(default_factory=list)

    def installer_argv(self) -> list[str]:
        argv = [SYSTEM_PYTHON, str(HELPER_DIR / "tools" / "install.py"), self.command, "--mode", "user",
                "--app", str(self.app)]
        if self.adhoc:
            argv.append("--allow-ad-hoc-for-lab")
            if self.command == "upgrade":
                argv.append("--skip-grant-check")  # a new ad-hoc signature never keeps the old grants
        elif self.command == "install":
            argv += ["--team", self.team or ""]
        return argv


class InstallStepError(Exception):
    pass


def plan(app: str | None, team: str | None, *, installed: bool) -> Plan:
    command = "upgrade" if installed else "install"
    if app:
        path = Path(app).expanduser().resolve()
        if command == "install" and not team:
            raise InstallStepError("a signed app needs its Developer ID team: pass --team TEAMID")
        return Plan(path, command, adhoc=False, team=team)
    return Plan(BUILT_APP, command, adhoc=True, notes=[
        "No signed app was given, so this is an ad-hoc build of this checkout. Its permissions do not carry over "
        "to the next ad-hoc build: after every update you switch them on again. Pass --app with a signed build "
        "to keep them."])


def missing_permissions(result: dict) -> list[str]:
    """Plain-language lines for every permission the helper reports as not granted (unknown keys are kept)."""
    lines = []
    for key, granted in sorted(result.items()):
        if granted is True or not isinstance(granted, bool):
            continue
        pane, purpose = PERMISSIONS.get(key, (key.replace("_", " ").title(), "its part of the helper's work"))
        lines.append(f"- {pane}: open System Settings > Privacy & Security > {pane}, then switch on "
                     f"LINA Helper (click + and pick /Applications/LINA Helper.app if it is not listed). "
                     f"This lets agents {purpose}.")
    return lines


def permission_report(raw: str | None) -> list[str]:
    try:
        value = json.loads(raw or "")
    except ValueError:
        value = None
    result = value.get("result") if isinstance(value, dict) and value.get("ok") is True else None
    if not isinstance(result, dict):
        return ["LINA Helper is installed but did not answer yet. Log in to this Mac's desktop (the helper starts "
                "with the desktop session), then run this again to see which permissions are missing."]
    missing = missing_permissions(result)
    if not missing:
        return ["LINA Helper has every permission it needs."]
    return ["LINA Helper still needs these permissions. A person at this Mac (or over Screen Sharing) switches "
            "them on once:", *missing]


def _run(argv, *, cwd=None, capture=False) -> subprocess.CompletedProcess:
    return subprocess.run(argv, cwd=cwd, text=True, encoding="utf-8", errors="replace", check=False, timeout=1800,
                          capture_output=capture, stdin=subprocess.DEVNULL)


def _build_adhoc(run) -> None:
    from hermes_platform.resolver import locate_command
    for tool in ("make", "swift"):
        if not locate_command(tool).command:
            raise InstallStepError(f"`{tool}` was not found. Install Xcode or the Command Line Tools "
                                   "(`xcode-select --install`), or pass --app with a built LINA Helper.app.")
    if run(["make", "-C", str(HELPER_DIR), "app"]).returncode != 0:
        raise InstallStepError("the LINA Helper build failed (see the output above)")


def install_helper(app: str | None = None, team: str | None = None, *, platform: str = sys.platform,
                   run=_run, out=print) -> int:
    if platform != "darwin":
        out("LINA Helper: skipped (it runs on macOS only).")
        return 0
    try:
        step = plan(app, team, installed=os.path.exists(APP))
        if step.adhoc:
            _build_adhoc(run)
        for note in step.notes:
            out(note)
        if run(step.installer_argv()).returncode != 0:
            raise InstallStepError(f"the LINA Helper {step.command} failed (see the output above)")
    except InstallStepError as e:
        out(f"LINA Helper: {e}")
        return 1
    out(f"LINA Helper: {step.command} done.")
    probe = run([CTL, "permissions", "--caller", "lina-install"], capture=True)
    for line in permission_report(probe.stdout if probe.returncode == 0 else None):
        out(line)
    return 0


def cmd_install(args) -> int:
    return install_helper(getattr(args, "helper_app", None), getattr(args, "helper_team", None))
