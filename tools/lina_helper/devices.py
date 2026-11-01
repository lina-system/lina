"""The device list: which Macs run LINA Helper and how to reach them (``devices`` in config.yaml).

```yaml
devices:
  default_mac: agent-hub        # computer_use target when a session has not chosen one
  macs:
    agent-hub: {host: local}    # the Mac LINA runs on: run lina-helperctl directly, no ssh
    agent-mac: {host: agent-mac, unlock: true}   # ssh alias; unlock tools allowed
```

Read from the active profile's config at call time, so each served profile sees its own list. ``local`` is a
name for this Mac even when nothing is configured, on macOS only.
"""

from __future__ import annotations

import re
import sys
from dataclasses import dataclass

NAME_RE = r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}"
LOCAL = "local"


class UnknownMac(LookupError):
    pass


@dataclass(frozen=True)
class Mac:
    name: str
    host: str  # "local" or an SSH alias
    unlock: bool = False

    @property
    def local(self) -> bool:
        return self.host == LOCAL


def _section() -> dict:
    from hermes_cli.config import load_config_readonly
    section = (load_config_readonly() or {}).get("devices")
    return section if isinstance(section, dict) else {}


def _entry(name, item) -> Mac | None:
    if not isinstance(name, str) or not re.fullmatch(NAME_RE, name) or not isinstance(item, dict):
        return None
    host = item.get("host")
    if not isinstance(host, str) or not host:
        return None
    return Mac(name, host, item.get("unlock") is True)


def configured(section: dict | None = None) -> dict[str, Mac]:
    """Every valid entry of ``devices.macs``, by name (invalid entries are left out)."""
    raw = (_section() if section is None else section).get("macs")
    if not isinstance(raw, dict):
        return {}
    return {mac.name: mac for name, item in raw.items() if (mac := _entry(name, item)) is not None}


def known(section: dict | None = None) -> dict[str, Mac]:
    macs = configured(section)
    if sys.platform == "darwin" and LOCAL not in macs:
        macs[LOCAL] = Mac(LOCAL, LOCAL)
    return macs


def default_name(section: dict | None = None) -> str:
    section = _section() if section is None else section
    name = section.get("default_mac")
    if isinstance(name, str) and name in known(section):
        return name
    macs = configured(section)
    first_local = next((m.name for m in macs.values() if m.local), None)
    return first_local or next(iter(macs), LOCAL)


def resolve(name: str, section: dict | None = None) -> Mac:
    """The configured Mac for ``name``; ``UnknownMac`` (naming the known ones) before anything runs."""
    macs = known(section)
    mac = macs.get(name) if isinstance(name, str) else None
    if mac is None:
        raise UnknownMac("unknown Mac name; use one of: %s" % (", ".join(sorted(macs)) or "(none configured)"))
    return mac
