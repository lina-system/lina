"""LINA's pre-rename gateway services, told apart from Hermes Agent's by the home they pin.

Two kinds of leftover share a name with Hermes Agent, which may run on the same account:

* ``hermes.service`` (systemd), from before the gateway unit carried a ``-gateway`` name.
* ``ai.hermes.gateway[-<profile>]`` LaunchAgents (macOS), LINA's label before #93.

The name says nothing about the owner. A leftover is LINA's only when it pins a LINA home
(``HERMES_HOME`` in the unit or plist); one pinning ``~/.hermes``, or no home at all, is Hermes
Agent's and is never stopped, rewritten or deleted.
"""

from __future__ import annotations

import os
import plistlib
import subprocess
from pathlib import Path

from hermes_constants import GATEWAY_LAUNCHD_LABEL_BASE, LEGACY_GATEWAY_LAUNCHD_LABEL_BASE, is_lina_home


def _gw():
    from hermes_cli import gateway  # late: the facade imports this module

    return gateway


def pins_lina_home(pinned: object) -> bool:
    """True when *pinned* (a unit's or plist's ``HERMES_HOME``) names a LINA home."""
    return isinstance(pinned, str) and bool(pinned.strip()) and is_lina_home(pinned.strip())


def unit_pins_lina_home(unit_path: Path) -> bool:
    """A systemd unit is LINA's only when its ``HERMES_HOME`` is a LINA home."""
    return pins_lina_home(_gw()._hermes_home_pinned_by_unit(unit_path))


def _legacy_label_suffix(label: object) -> str | None:
    """``""`` / ``"-<profile>"`` for an ``ai.hermes.gateway[-<profile>]`` label, else None."""
    if not isinstance(label, str) or not label.startswith(LEGACY_GATEWAY_LAUNCHD_LABEL_BASE):
        return None
    suffix = label[len(LEGACY_GATEWAY_LAUNCHD_LABEL_BASE):]
    return suffix if suffix == "" or suffix.startswith("-") else None


def find_legacy_launchd_agents() -> list[tuple[str, Path, str]]:
    """``[(old_label, plist_path, new_label)]`` for LINA's ``ai.hermes.gateway*`` LaunchAgents.

    Read-only. A plist that is unreadable, carries another label, or pins no LINA home is skipped.
    """
    agents_dir = _gw().get_launchd_plist_path().parent
    if not agents_dir.is_dir():
        return []
    found: list[tuple[str, Path, str]] = []
    for plist_path in sorted(agents_dir.glob(f"{LEGACY_GATEWAY_LAUNCHD_LABEL_BASE}*.plist")):
        try:
            data = plistlib.loads(plist_path.read_bytes())
        except (OSError, ValueError, plistlib.InvalidFileException):
            continue
        if not isinstance(data, dict):
            continue
        label = data.get("Label")
        suffix = _legacy_label_suffix(label)
        env = data.get("EnvironmentVariables")
        if suffix is None or not isinstance(env, dict) or not pins_lina_home(env.get("HERMES_HOME")):
            continue
        found.append((label, plist_path, f"{GATEWAY_LAUNCHD_LABEL_BASE}{suffix}"))
    return found


def print_legacy_launchd_warning() -> None:
    """Name LINA's ``ai.hermes.gateway*`` LaunchAgents: each still starts a second gateway at login."""
    legacy = find_legacy_launchd_agents()
    if not legacy:
        return
    print("⚠ LINA gateway LaunchAgent(s) under the old ai.hermes.gateway label:")
    for _, path, _ in legacy:
        print(f"    {path}")
    print("  They start a second gateway for the same home. Move them with: lina gateway migrate-legacy")


def _launchctl(*args: str) -> subprocess.CompletedProcess:
    return subprocess.run(["launchctl", *args], check=False, timeout=90, capture_output=True,
                          text=True, encoding="utf-8", errors="replace")


def _loaded_domain(label: str) -> str | None:
    """The launchd domain (``gui/<uid>`` or ``user/<uid>``) that has *label* loaded, else None."""
    uid = os.getuid()  # windows-footgun: ok — macOS LaunchAgent helper, never invoked on Windows
    for domain in (f"gui/{uid}", f"user/{uid}"):
        if _launchctl("print", f"{domain}/{label}").returncode == 0:
            return domain
    return None


def _move_agent(old_label: str, plist_path: Path, new_label: str) -> None:
    """Re-register one LaunchAgent under LINA's label, running again only if it was loaded."""
    data = plistlib.loads(plist_path.read_bytes())
    target = plist_path.with_name(f"{new_label}.plist")
    domain = _loaded_domain(old_label)
    if domain is not None:
        _launchctl("bootout", f"{domain}/{old_label}")
    if target.exists():
        print(f"  {target.name} is already installed; removing the old agent only")
    else:
        data["Label"] = new_label
        target.write_bytes(plistlib.dumps(data))
        if domain is not None:
            done = _launchctl("bootstrap", domain, str(target))
            if done.returncode != 0:
                raise OSError(f"launchctl bootstrap {domain} {target} failed: {(done.stderr or '').strip()}")
    plist_path.unlink()


def migrate_legacy_launchd_agents(interactive: bool = True, dry_run: bool = False) -> tuple[int, list[Path]]:
    """Move LINA's ``ai.hermes.gateway*`` LaunchAgents to ``ai.lina.gateway*``; returns
    ``(moved_count, remaining_paths)``. Hermes Agent's agents are never listed, so never touched."""
    legacy = find_legacy_launchd_agents()
    if not legacy:
        print("No LINA gateway LaunchAgents under the old ai.hermes.gateway label.")
        return 0, []
    print()
    print("LINA gateway LaunchAgent(s) under the old ai.hermes.gateway label:")
    for old_label, path, new_label in legacy:
        print(f"  {path}  ({old_label} -> {new_label})")
    print()
    if dry_run:
        print("(dry-run — nothing changed)")
        return 0, [path for _, path, _ in legacy]
    from hermes_cli.setup import prompt_yes_no
    if interactive and not prompt_yes_no("Move them to LINA's labels?", True):
        print("Skipped. Run again with: lina gateway migrate-legacy")
        return 0, [path for _, path, _ in legacy]
    moved = 0
    remaining: list[Path] = []
    for old_label, path, new_label in legacy:
        try:
            _move_agent(old_label, path, new_label)
        except (OSError, ValueError, subprocess.SubprocessError) as exc:
            print(f"  ⚠ Could not move {path}: {exc}")
            remaining.append(path)
            continue
        print(f"  ✓ {old_label} -> {new_label}")
        moved += 1
    return moved, remaining


def migrate_legacy(*, interactive: bool, dry_run: bool) -> None:
    """``lina gateway migrate-legacy``: systemd ``hermes.service`` and macOS ``ai.hermes.gateway*``."""
    gw = _gw()
    if gw.is_macos():
        migrate_legacy_launchd_agents(interactive=interactive, dry_run=dry_run)
    elif gw.supports_systemd_services():
        gw.remove_legacy_hermes_units(interactive=interactive, dry_run=dry_run)
    else:
        print("Legacy unit migration only applies to systemd-based Linux hosts and macOS.")
