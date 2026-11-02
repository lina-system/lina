"""LINA's pre-rename gateway services, told apart from Hermes Agent's by the home they pin.

Two kinds of leftover share a name with Hermes Agent, which may run on the same account:

* ``hermes.service`` (systemd), from before the gateway unit carried a ``-gateway`` name.
* ``ai.hermes.gateway[-<profile>]`` LaunchAgents (macOS), LINA's label before #93.

The name says nothing about the owner. A leftover is LINA's only when it pins a LINA home
(``HERMES_HOME`` in the unit or plist); one pinning ``~/.hermes``, or no home at all, is Hermes
Agent's and is never stopped, rewritten or deleted.
"""

from __future__ import annotations

import contextlib
import plistlib
import re
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


def _candidate_legacy_labels() -> tuple[list[str], set[str]]:
    """``(labels, derived)``: the old labels this install's profile layout maps to (``derived``, as
    ``launchd_gateway_labels_for_install`` derives LINA's current ones) plus any ``ai.hermes.gateway*``
    label launchd has loaded (the listing the facade's prefix scan reads), e.g. a pre-suffix one."""
    derived = {LEGACY_GATEWAY_LAUNCHD_LABEL_BASE + label[len(GATEWAY_LAUNCHD_LABEL_BASE):]
               for label in _gw().launchd_gateway_labels_for_install()}
    labels = set(derived)
    try:
        listed = subprocess.run(["launchctl", "list"], check=False, timeout=10, **_gw()._CAPTURE_TEXT)
        lines = listed.stdout.splitlines() if listed.returncode == 0 else []
    except (OSError, subprocess.TimeoutExpired):
        lines = []
    labels.update(parts[-1] for parts in map(str.split, lines)
                  if len(parts) >= 3 and _legacy_label_suffix(parts[-1]) is not None)
    return sorted(labels), derived


def _loaded_definition_path(label: str) -> Path | None:
    """The plist launchd loaded *label* from, looked up in ``gui/<uid>`` then ``user/<uid>``; None when
    the label is loaded in neither domain or its definition path cannot be read."""
    try:
        domain, _pid = _gw()._locate_launchd_gateway_service(label)
        if domain is None:
            return None
        printed = _launchctl("print", f"{domain}/{label}")
    except (OSError, subprocess.TimeoutExpired):
        return None
    match = re.search(r"^\tpath = (.+)$", printed.stdout or "", re.MULTILINE)
    return Path(match.group(1).strip()) if printed.returncode == 0 and match else None


def _is_lina_legacy_definition(plist_path: Path, label: str) -> bool:
    """The plist defines *label*, runs a gateway, and pins a LINA home in its environment."""
    try:
        data = plistlib.loads(plist_path.read_bytes())
    except (OSError, ValueError, plistlib.InvalidFileException):
        return False
    if not isinstance(data, dict) or data.get("Label") != label:
        return False
    argv, env = data.get("ProgramArguments"), data.get("EnvironmentVariables")
    runs_gateway = isinstance(argv, list) and any(isinstance(arg, str) and "gateway" in arg for arg in argv)
    return runs_gateway and isinstance(env, dict) and pins_lina_home(env.get("HERMES_HOME"))


def find_legacy_launchd_agents() -> list[tuple[str, Path, str]]:
    """``[(old_label, plist_path, new_label)]`` for LINA's ``ai.hermes.gateway*`` LaunchAgents. Read-only.

    A loaded label is judged by the definition launchd loaded it from; a derived label that is not
    loaded by its plist in the LaunchAgents folder. Never a folder glob: a plist that carries another
    label, runs no gateway or pins no LINA home (Hermes Agent's ``~/.hermes``) is not LINA's.
    """
    agents_dir = _gw().get_launchd_plist_path().parent
    labels, derived = _candidate_legacy_labels()
    found: list[tuple[str, Path, str]] = []
    for label in labels:
        plist_path = _loaded_definition_path(label)
        if plist_path is None and label in derived:
            plist_path = agents_dir / f"{label}.plist"
        if plist_path is not None and _is_lina_legacy_definition(plist_path, label):
            found.append((label, plist_path, f"{GATEWAY_LAUNCHD_LABEL_BASE}{_legacy_label_suffix(label)}"))
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
    try:
        return _gw()._locate_launchd_gateway_service(label)[0]
    except subprocess.TimeoutExpired:
        return None


def _move_agent(old_label: str, plist_path: Path, new_label: str) -> None:
    """Re-register one LaunchAgent under LINA's label. A loaded old job is replaced by a loaded new one
    (an existing but unloaded ``ai.lina.gateway*`` plist is bootstrapped, not trusted); the old
    definition is removed only once the new label is loaded, else the old job is restored."""
    data = plistlib.loads(plist_path.read_bytes())
    target = _gw().get_launchd_plist_path().parent / f"{new_label}.plist"
    if not target.exists():
        data["Label"] = new_label
        target.write_bytes(plistlib.dumps(data))
    domain = _loaded_domain(old_label)
    if domain is not None:
        _launchctl("bootout", f"{domain}/{old_label}")
        if _loaded_domain(new_label) is None:
            with contextlib.suppress(subprocess.SubprocessError, OSError):
                _gw()._launchctl_bootstrap(domain, target, new_label)
        if _loaded_domain(new_label) is None:
            _launchctl("bootstrap", domain, str(plist_path))
            raise OSError(f"{new_label} did not load from {target}; {old_label} was restored")
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
