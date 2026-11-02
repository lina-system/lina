"""Registers the LINA Helper tools (toolset ``lina_helper``): Mac target, status, screenshot, screen unlock."""

from __future__ import annotations

from tools.lina_helper import actions, devices
from tools.registry import registry

TOOLSET = "lina_helper"

_MAC = {"type": "string", "description": "Mac name from the device list (devices.macs in config.yaml)."}
_OPTIONAL_MAC = dict(_MAC, description="Mac name from the device list. Default: this session's desktop-control target.")


def _schema(name: str, description: str, properties: dict, required: list) -> dict:
    return {"name": name, "description": description,
            "parameters": {"type": "object", "properties": properties, "required": required,
                           "additionalProperties": False}}


def helper_available() -> bool:
    """A Mac is reachable by name: one is configured, or LINA runs on a Mac."""
    return bool(devices.known())


def unlock_available() -> bool:
    return any(mac.unlock and not mac.local for mac in devices.configured().values())


def _session(kw: dict) -> str:
    return str(kw.get("session_id") or "")


_HELPER_TOOLS = (
    ("mac_use",
     "Choose which Mac this session's desktop control acts on, by name. Changing it restarts the session's "
     "desktop-control connection on the new Mac; capture again afterwards.",
     {"mac": _MAC}, ["mac"],
     lambda args, kw: actions.mac_use(args, _session(kw))),
    ("mac_status",
     "LINA Helper on a Mac: version, permission grants, policy profile, cua-driver state and who holds the input "
     "lease. Also reports this session's desktop-control target.",
     {"mac": _OPTIONAL_MAC}, [],
     lambda args, kw: actions.mac_status(args, _session(kw))),
    ("mac_screenshot",
     "Full-screen screenshot of a Mac through LINA Helper. Returns a local PNG path "
     "with width, height and bytes (no desktop-control session needed).",
     {"mac": _OPTIONAL_MAC,
      "display": {"type": ["string", "integer"], "description": '"main" (default) or a display id.'}}, [],
     lambda args, kw: actions.mac_screenshot(args, _session(kw))),
)

_UNLOCK_TOOLS = (
    ("mac_unlock_status", "status",
     "Inspect the screen-unlock service on a Mac. This does not unlock or install anything."),
    ("mac_unlock", "unlock",
     "Request one screen unlock on a Mac. Success requires the exact grant receipt, an unlocked session, both "
     "keychain probes and an active display. It does not validate FaceTime or reboot recovery."),
    ("mac_unlock_revoke", "revoke",
     "Cancel any outstanding short-lived unlock authorization on a Mac."),
)


def _register_helper(name, description, properties, required, handler):
    registry.register(
        name=name, toolset=TOOLSET, schema=_schema(name, description, properties, required),
        handler=lambda args, **kw: actions.call(lambda a: handler(a, kw), args,
                                                mac_label=args.get("mac") if isinstance(args, dict) else None),
        check_fn=helper_available, requires_env=[])


def _register_unlock(name, operation, description):
    registry.register(
        name=name, toolset=TOOLSET, schema=_schema(name, description, {"mac": _MAC}, ["mac"]),
        handler=lambda args, **kw: actions.call(lambda a: actions.mac_unlock(operation, a), args,
                                                mac_label=args.get("mac") if isinstance(args, dict) else None),
        check_fn=unlock_available, requires_env=[])


for _spec in _HELPER_TOOLS:
    _register_helper(*_spec)
for _spec in _UNLOCK_TOOLS:
    _register_unlock(*_spec)
