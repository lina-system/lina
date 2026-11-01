"""Tool logic: the per-session computer_use target, helper requests by Mac name, unlock requests.

Every handler returns a JSON string and never raises: ``{"ok": true, "mac": NAME, ...}`` or
``{"ok": false, "error": {"code", "detail"}, "mac": NAME}``. The helper's policy on each Mac decides what is
allowed; this side only refuses unknown names and malformed arguments before any subprocess runs.
"""

from __future__ import annotations

import json
import os
import subprocess
import threading
import uuid

from tools.lina_helper import client, devices, unlock


class Refused(Exception):
    def __init__(self, code: str, detail: str = ""):
        super().__init__(code)
        self.code, self.detail = code, detail


class Targets:
    """Which Mac each session's computer_use acts on (process memory; a session without a choice gets the default)."""

    def __init__(self):
        self._lock = threading.Lock()
        self._by_session: dict[str, str] = {}

    def set(self, session_id: str, mac: str) -> None:
        with self._lock:
            self._by_session[session_id or ""] = mac

    def get(self, session_id: str) -> str:
        with self._lock:
            chosen = self._by_session.get(session_id or "")
        return chosen or devices.default_name()


targets = Targets()


def fail(mac, code: str, detail: str = "") -> dict:
    return {"ok": False, "error": {"code": code, "detail": detail}, "mac": mac}


def cache_dir() -> str:
    from hermes_constants import get_hermes_home
    return os.path.join(str(get_hermes_home()), "cache", "lina-helper")


def resolve(name) -> devices.Mac:
    try:
        return devices.resolve(name)
    except devices.UnknownMac as e:
        raise Refused("unknown_mac", str(e)) from None


def release_backend(session_id: str) -> bool:
    from tools.computer_use.tool import release_computer_use_session
    return release_computer_use_session(session_id)


class _Recorder:
    """Keeps the last completed process so an ssh failure (exit 255) is told apart from a bad helper reply."""

    def __init__(self, runner):
        self.runner, self.last = runner, None

    def __call__(self, cmd, **kw):
        self.last = self.runner(cmd, **kw)
        return self.last


def _stderr_line(p) -> str:
    lines = (p.stderr or b"").decode("utf-8", "replace").strip().splitlines()
    return lines[-1][:300] if lines else ""


def _error_from(value: dict, mac: devices.Mac, last) -> dict:
    error = value.get("error") or {}
    detail = error.get("detail") or ""
    code = error.get("code") or "internal"
    if code == "transport_timeout":
        code = "timeout"
    if code == "invalid_remote_response" and last is not None and not mac.local and last.returncode == 255:
        code, detail = "ssh_failed", _stderr_line(last)
    return fail(mac.name, code, detail if isinstance(detail, str) else json.dumps(detail))


def _run(cmd, **kw):
    return subprocess.run(cmd, stdin=kw.pop("stdin", subprocess.DEVNULL), check=kw.pop("check", False), **kw)


def helper_action(name: str, action: str, typed: dict, *, screenshot: bool = False) -> dict:
    """One lina-helperctl request on a Mac by name; a result dict, never raises."""
    try:
        mac = resolve(name)
        return _helper(mac, action, typed, screenshot)
    except Refused as r:
        return fail(name, r.code, r.detail)


def _screenshot_path(mac: devices.Mac) -> str:
    directory = cache_dir()
    try:
        os.makedirs(directory, mode=0o700, exist_ok=True)
    except OSError as e:
        raise Refused("output_unwritable", e.strerror or "cache directory not writable") from None
    return os.path.join(directory, f"mac-{mac.name}-{uuid.uuid4().hex}.png")


def _helper(mac: devices.Mac, action: str, typed: dict, screenshot: bool) -> dict:
    out = _screenshot_path(mac) if screenshot else None
    rec = _Recorder(_run)
    try:
        code, value = client.invoke(mac.host, action, out=out, runner=rec,
                                    json_args=json.dumps(typed) if typed else None, local=mac.local,
                                    connect_timeout=10)
    except client.UsageError as e:
        raise Refused("bad_request", str(e)) from None
    except OSError as e:
        raise Refused("exec_failed" if mac.local else "ssh_failed", e.strerror or str(e)) from None
    if value.get("ok") is not True or code != 0:
        return _error_from(value, mac, rec.last)
    result = value.get("result") or {}
    if out is None:
        return {"ok": True, "mac": mac.name, "result": result}
    return {"ok": True, "mac": mac.name, "path": out, "bytes": os.path.getsize(out),
            "width": result.get("width"), "height": result.get("height")}


def mac_use(args: dict, session_id: str) -> dict:
    mac = args.get("mac")
    if not isinstance(mac, str):
        raise Refused("bad_request", "mac is required")
    resolve(mac)
    previous = targets.get(session_id)
    targets.set(session_id, mac)
    if previous != mac:
        release_backend(session_id)
    return {"ok": True, "mac": mac, "previous": previous, "note": f"computer_use in this session now acts on {mac}"}


def mac_status(args: dict, session_id: str) -> dict:
    mac = args.get("mac") or targets.get(session_id)
    status = helper_action(mac, "status", {})
    if status.get("ok"):
        cua = helper_action(mac, "cua.status", {})
        if cua.get("ok"):
            status["result"]["cua"] = cua["result"]
        status["computer_use_target"] = targets.get(session_id)
    return status


def mac_screenshot(args: dict, session_id: str) -> dict:
    mac = args.get("mac") or targets.get(session_id)
    display = args.get("display", "main")
    if display not in (None, "main") and (not isinstance(display, int) or isinstance(display, bool) or display < 1):
        raise Refused("bad_request", 'display must be "main" or a display id')
    typed = {} if display in (None, "main") else {"display": display}
    return helper_action(mac, "screenshot", typed, screenshot=True)


def mac_unlock(operation: str, args: dict) -> dict:
    """``status`` / ``unlock`` / ``revoke`` on a Mac whose device entry has ``unlock: true`` (SSH only)."""
    name = args.get("mac")
    if not isinstance(name, str) or set(args) != {"mac"}:
        raise Refused("bad_request", "mac is the only argument")
    mac = resolve(name)
    if not mac.unlock:
        raise Refused("not_allowed", f"unlock is not enabled for this Mac (devices.macs.{mac.name}.unlock)")
    if mac.local:
        raise Refused("not_allowed", "unlock reaches another Mac over SSH; this entry is the local Mac")
    try:
        code, result = unlock.invoke(mac.host, operation, runner=_run)
    except (ValueError, OSError) as e:
        raise Refused("ssh_failed", str(e)) from None
    return {**result, "ok": code == 0, "success": code == 0, "mac": mac.name}


def call(handler, args, *, mac_label=None) -> str:
    """Run one tool handler; JSON string for the registry, never raises."""
    label = mac_label if isinstance(mac_label, str) and len(mac_label) <= 64 else None
    if not isinstance(args, dict):
        value = fail(label, "bad_request", "arguments must be an object")
    else:
        try:
            value = handler(args)
        except Refused as r:
            value = fail(label, r.code, r.detail)
    return json.dumps(value, sort_keys=True)
