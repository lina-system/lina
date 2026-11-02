"""One ``lina-helperctl`` request, run on this Mac or on another Mac over SSH. Standard library only.

``lina-helperctl ACTION [--arg k=v]... [--json OBJECT] [--out -]`` prints one JSON response line and exits 0
only on ``ok: true``. With ``--out -`` (screenshot only) the file bytes go to stdout and the JSON response is
the last line of stderr. Nothing here retries.
"""

from __future__ import annotations

import json
import os
import re
import shlex
import subprocess

CTL = "/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl"
ACTIONS = ("status", "permissions", "screenshot", "ax.tree", "cua.status", "cua.restart", "cua.lease",
           "cua.release")
HOST_RE = r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}"
KEY_RE = r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}"
# Same ceiling as lina-helperctl's response frame (helper/macos/app Transport.responseLimit).
MAX_JSON = 128 << 20
TIMEOUT = 30
SCREENSHOT_TIMEOUT = 45


class UsageError(ValueError):
    pass


def failure(code: str, detail: str | None = None) -> dict:
    error = {"code": code}
    if detail:
        error["detail"] = detail
    return {"v": 1, "ok": False, "error": error}


def ssh_argv(host: str, remote: list[str], *, connect_timeout: int = 8) -> list[str]:
    """``ssh`` argv running ``remote`` (quoted with ``shlex.join``) on ``host``; never a caller-built shell string."""
    if not re.fullmatch(HOST_RE, host or ""):
        raise UsageError("host must be a configured SSH alias or hostname")
    return ["ssh", "-T", "-o", "BatchMode=yes", "-o", f"ConnectTimeout={connect_timeout}",
            "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=2", host, shlex.join(remote)]


def command(host, action, args=(), out=False, json_args=None, local=False, connect_timeout=8):
    """argv for one lina-helperctl call: over ssh, or run directly when local (host unused)."""
    if action not in ACTIONS:
        raise UsageError("unsupported action")
    if out and action != "screenshot":
        raise UsageError("--out is only valid for screenshot")
    remote = [CTL, action]
    for item in args:
        key, sep, _ = item.partition("=")
        if not sep or not re.fullmatch(KEY_RE, key):
            raise UsageError("--arg must be k=v with a plain key")
        remote += ["--arg", item]
    if json_args is not None:
        try:
            value = json.loads(json_args)
        except ValueError:
            raise UsageError("--json must be a JSON object") from None
        if not isinstance(value, dict):
            raise UsageError("--json must be a JSON object")
        remote += ["--json", json.dumps(value, separators=(",", ":"), sort_keys=True)]
    if out:
        remote += ["--out", "-"]
    if local:
        return remote
    return ssh_argv(host, remote, connect_timeout=connect_timeout)


def parse_response(raw: bytes):
    if len(raw) > MAX_JSON:
        return None
    try:
        value = json.loads(raw)
    except (ValueError, UnicodeError):
        return None
    if not isinstance(value, dict) or value.get("v") != 1 or not isinstance(value.get("ok"), bool):
        return None
    return value


def _write_exclusive(out: str, data: bytes):
    """Create ``out`` exclusively and write ``data``; a failure dict, or None. Leaves no partial file."""
    try:
        f = open(out, "xb")
    except FileExistsError:
        raise UsageError("refusing to overwrite existing file") from None
    except OSError as e:
        return failure("output_unwritable", e.strerror or str(e))
    try:
        with f:
            f.write(data)
    except OSError as e:
        try:
            os.unlink(out)
        except OSError:
            pass
        return failure("output_unwritable", e.strerror or str(e))
    return None


def invoke(host, action, args=(), out=None, runner=subprocess.run, json_args=None, local=False,
           connect_timeout=8):
    """Return (exit_code, response dict). Never retries."""
    cmd = command(host, action, args, out is not None, json_args, local, connect_timeout)
    if out is not None and os.path.lexists(out):
        raise UsageError("refusing to overwrite existing file")
    try:
        p = runner(cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                   timeout=SCREENSHOT_TIMEOUT if action == "screenshot" else TIMEOUT, check=False)
    except subprocess.TimeoutExpired:
        return 1, failure("transport_timeout")
    bad = (1, failure("invalid_remote_response"))
    if out is None:
        value = parse_response(p.stdout)
    else:
        value = parse_response(p.stderr.rstrip(b"\r\n").split(b"\n")[-1])
    if value is None:
        return bad
    if not value["ok"]:
        return 1, value
    if p.returncode != 0:
        return bad
    if out is not None:
        if not p.stdout:
            return bad
        problem = _write_exclusive(out, p.stdout)
        if problem is not None:
            return 1, problem
    return 0, value
