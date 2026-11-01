"""Screen unlock on another Mac: one bounded ``sudo -n hermes-unlockctl`` call over SSH.

The unlock package (``helper/macos/unlock``) installs ``hermes-unlockctl`` and a sudoers entry for it on the
target Mac. No passwords, shell scripts or installs happen from here. ``unlock`` succeeds only with the exact
grant receipt, an unlocked session, both keychain probes and an active display.
"""

from __future__ import annotations

import json
import subprocess

from tools.lina_helper.client import ssh_argv

CTL = "/Library/Application Support/HermesUnlock/bin/hermes-unlockctl"
OPERATIONS = ("status", "unlock", "revoke")
MAX_RESPONSE = 16384
TIMEOUT = 25


def command(host: str, operation: str) -> list[str]:
    if operation not in OPERATIONS:
        raise ValueError("unsupported operation")
    return ssh_argv(host, ["sudo", "-n", CTL, operation])


def _unlocked(value: dict, returncode: int) -> bool:
    numeric = ("legacy_keychain_status", "data_protection_status", "active_displays")
    return (all(type(value.get(k)) is int for k in numeric) and returncode == 0
            and value.get("desktop_ready") is True and value.get("screen_unlocked") is True
            and value.get("legacy_keychain_status") == 0 and value.get("data_protection_status") == 0
            and value.get("active_displays", 0) > 0
            and value.get("result") in ("unlocked", "already_unlocked")
            and (value.get("result") == "already_unlocked" or value.get("authorization_consumed") is True))


def invoke(host: str, operation: str, runner=subprocess.run) -> tuple[int, dict]:
    """(exit code, response). ``desktop_ready`` is forced False unless every unlock postcondition holds."""
    try:
        p = runner(command(host, operation), stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                   stderr=subprocess.PIPE, timeout=TIMEOUT, check=False)
    except subprocess.TimeoutExpired:
        return 1, {"schema_version": 1, "result": "transport_timeout", "desktop_ready": False}
    if len(p.stdout) > MAX_RESPONSE:
        return 1, {"schema_version": 1, "result": "invalid_remote_response", "desktop_ready": False}
    try:
        value = json.loads(p.stdout)
        if not isinstance(value, dict) or value.get("schema_version") != 1 or not isinstance(value.get("result"), str):
            raise ValueError("unexpected response")
    except (ValueError, UnicodeError):
        return 1, {"schema_version": 1, "result": "ssh_or_sudo_failed" if p.returncode else "invalid_remote_response",
                   "desktop_ready": False, "remote_exit_code": p.returncode}
    if operation == "unlock":
        success = _unlocked(value, p.returncode)
        if not success:
            value["desktop_ready"] = False
        return (0 if success else 1), value
    return (0 if p.returncode == 0 else 1), value
