"""Each agent's gateway key (lina#643).

A named profile's api_server door accepts only that profile's own ``API_SERVER_KEY`` (``APIServerAdapter.
_expected_api_key``, fail closed), and the web server's talk routes (``hermes_cli/lina_talk.py``) send
:func:`web_server_key`. Creating an agent (``lina migrate memory``, ``hermes profile create``) writes both sides: a
fresh key in the profile's ``.env`` when it has none, and the same value under :func:`key_env` in the web server's own
``.env`` (the LINA root) when that variable is unset. An existing value on either side is left alone; ``lina doctor``
names the profiles whose two keys then differ. A key value is never printed or logged.
"""

from __future__ import annotations

import hmac
import os
import re
import secrets
from pathlib import Path
from typing import Iterable

PROFILE_KEY = "API_SERVER_KEY"


def key_env(agent: str) -> str:
    """The web server's variable for *agent*'s key: ``LINA_GATEWAY_KEY_<AGENT>``, the name in upper case with each
    character a variable name cannot hold (the ``-`` of ``alpha-copy``) as ``_``."""
    return "LINA_GATEWAY_KEY_" + re.sub(r"[^A-Z0-9_]", "_", agent.upper())


def _value(env_path: Path, key: str) -> str:
    from agent.secret_scope import load_env_file
    return (load_env_file(env_path).get(key) or "").strip()


def web_server_key(agent: str, home: Path) -> str:
    """The key the web server whose home is *home* sends to *agent*'s gateway: :func:`key_env` from its environment,
    else from its own ``.env`` (written by an agent created after it started), else its shared ``API_SERVER_KEY``.
    Never the profile's ``.env``: the web server reads only its own configuration."""
    from agent.secret_scope import load_env_file
    name = key_env(agent)
    key = os.environ.get(name)
    if key is None:
        key = load_env_file(home / ".env").get(name)
    if key is None:
        key = os.environ.get(PROFILE_KEY, "")
    return key.strip()


def _set_if_missing(env_path: Path, key: str, value: str, *, private: bool) -> str:
    """*key*'s value in *env_path*, written as *value* first when unset or empty; *private* makes the file 0600."""
    from hermes_cli.config import (
        _env_line_defines_key, _env_write_lock, _quote_env_value, _read_env_lines, _write_env_lines,
    )
    with _env_write_lock(env_path):
        current = _value(env_path, key)
        if current:
            return current
        try:
            existing = _read_env_lines(env_path)
        except FileNotFoundError:
            existing = []
        lines = [line for line in existing if not _env_line_defines_key(line, key)]
        lines.append(f"{key}={_quote_env_value(value)}\n")
        _write_env_lines(env_path, lines, preserve_mode=not private)
    return value


def ensure_profile_key(profile_dir: Path) -> None:
    _set_if_missing(profile_dir / ".env", PROFILE_KEY, secrets.token_urlsafe(32), private=True)


def publish_web_server_key(root: Path, agent: str, profile_dir: Path) -> None:
    """The profile's key under :func:`key_env` in the web server's ``.env`` at *root*, unless that is already set."""
    key = _value(profile_dir / ".env", PROFILE_KEY)
    if key:
        _set_if_missing(root / ".env", key_env(agent), key, private=False)


def provision(profile_dir: Path, agent: str, root: Path) -> None:
    ensure_profile_key(profile_dir)
    publish_web_server_key(root, agent, profile_dir)


def same_env_but_key(source: Path, target: Path) -> bool:
    """True when the copied *target* ``.env`` is *source* plus the key :func:`ensure_profile_key` added, so a
    migration re-run accepts its own earlier copy."""
    from hermes_cli.config import _env_line_defines_key
    if _value(source, PROFILE_KEY):
        return False
    def rest(path: Path) -> list[str]:
        return [line for line in path.read_text(encoding="utf-8").splitlines()
                if not _env_line_defines_key(line, PROFILE_KEY)]
    return rest(source) == rest(target)


def mismatched(root: Path, profiles: Iterable[tuple[str, Path]]) -> list[tuple[str, str]]:
    """``(agent, reason)`` for each ``(agent, profile dir)`` whose gateway would refuse the key the web server at
    *root* sends; names only."""
    found = []
    for agent, profile_dir in profiles:
        own = _value(profile_dir / ".env", PROFILE_KEY)
        if not own:
            found.append((agent, f"no {PROFILE_KEY} in its .env"))
        elif not hmac.compare_digest(own.encode(), web_server_key(agent, root).encode()):
            found.append((agent, f"its {PROFILE_KEY} differs from the web server's {key_env(agent)}"))
    return found
