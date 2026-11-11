"""Who may write the work settings through the client API (lina#256): the owner, from one of the owner's devices,
and the owner's agent nodes (lina#574).

A write passes only when all of these hold:

1. the dashboard credential is valid (the dashboard's own auth gate, before any route runs);
2. the peer's tailnet identity, from ``<tailscale> whois --json <peer ip>``, has a device name in the configured
   agents list (whatever its login: a tagged node such as the hub has no user login), or has the configured owner
   login and a device name in the configured owner device list;
3. that device made at most 30 writes in the last minute.

```yaml
worker:
  settings:
    owner:
      login: <tailnet login>
      devices: [<device name>, ...]   # the owner's own devices: need the owner login
      agents: [<device name>, ...]    # nodes of the owner's fleet that run agents (the hub): any login
    tailscale: tailscale            # the command (a path or an argv list); default tailscale
```

Any whois error refuses the write; reads never depend on it. With neither an owner login with devices nor an agent
configured nobody writes through the API (the settings view says ``writable: false``); the CLI on the hub always
writes, so a listed agent node gets no more than its own CLI already has. These keys are not work settings, so the
API can never change who may write.
"""

from __future__ import annotations

import json
import subprocess
import threading
import time
from pathlib import Path

RATE = 30
WINDOW_S = 60
VIEWER_S = 60


def _names(value) -> list[str]:
    return [str(d).lower() for d in value if d] if isinstance(value, list) else []


def _owner(base: Path | None) -> tuple[str, list[str], list[str], list[str]]:
    """``(login, devices, agents, tailscale argv)``; device names lower-cased."""
    from hermes_cli.worker import settings
    conf = settings.section(base).get("settings")
    conf = conf if isinstance(conf, dict) else {}
    owner = conf.get("owner") if isinstance(conf.get("owner"), dict) else {}
    tool = conf.get("tailscale") or "tailscale"
    argv = [str(part) for part in tool] if isinstance(tool, list) else [str(tool)]
    return str(owner.get("login") or ""), _names(owner.get("devices")), _names(owner.get("agents")), argv


def whois(argv: list[str], ip: str) -> tuple[str, str] | None:
    """``(login, device)`` for a tailnet address; None when tailscale cannot say."""
    try:
        done = subprocess.run([*argv, "whois", "--json", ip], capture_output=True, text=True, encoding="utf-8",
                              errors="replace", stdin=subprocess.DEVNULL, check=True, timeout=5)
        value = json.loads(done.stdout)
        node, user = value.get("Node") or {}, value.get("UserProfile") or {}
        device = node.get("ComputedName") or str(node.get("Name") or "").split(".")[0] \
            or (node.get("Hostinfo") or {}).get("Hostname") or ""
        return str(user.get("LoginName") or ""), str(device).lower()
    except (OSError, ValueError, AttributeError, subprocess.SubprocessError):
        return None


class Gate:
    def __init__(self):
        self.lock = threading.Lock()
        self.hits: dict[str, list[float]] = {}
        self.seen: dict[tuple, tuple[float, str]] = {}

    def reset(self) -> None:
        with self.lock:
            self.hits.clear()
            self.seen.clear()

    def owner_device(self, ip: str, base: Path | None = None) -> tuple[str, str]:
        """``(login, device)`` when *ip* is a listed agent node (any login) or the owner on a listed device, else
        ``("", "")``."""
        login, devices, agents, argv = _owner(base)
        if not ip or not (agents or (login and devices)):
            return "", ""
        who = whois(argv, ip)
        if not who or not (who[1] in agents or (login and who[0] == login and who[1] in devices)):
            return "", ""
        return who

    def writable(self, ip: str, base: Path | None = None, now: float | None = None) -> bool:
        """For the view only: may this reader write? Kept a minute, so polling does not ask tailscale each time."""
        now = time.monotonic() if now is None else now
        key = (ip, str(base))
        with self.lock:
            hit = self.seen.get(key)
            if hit and now - hit[0] < VIEWER_S:
                return bool(hit[1])
        device = self.owner_device(ip, base)[1]
        with self.lock:
            self.seen[key] = (now, device)
        return bool(device)

    def check(self, ip: str, base: Path | None = None, now: float | None = None) -> tuple[dict | None, int, str]:
        """``(by, 0, "")`` when the write may go ahead, else ``(None, status, reason)``."""
        now = time.monotonic() if now is None else now
        login, device = self.owner_device(ip, base)
        if not device:
            return None, 403, "settings are written only from the owner's devices and agent nodes " \
                              "(see worker.settings.owner)"
        with self.lock:
            hits = [at for at in self.hits.get(device, []) if now - at < WINDOW_S]
            if len(hits) >= RATE:
                self.hits[device] = hits
                return None, 429, f"too many writes: {RATE} a minute per device"
            self.hits[device] = hits + [now]
        return {"kind": "app", "principal": login, "device": device}, 0, ""


GATE = Gate()
