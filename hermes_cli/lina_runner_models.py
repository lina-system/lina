"""Where a runner block's model list comes from and how it is refreshed (lina#531).

``models: {file: <path>, refresh: {ocx_client: <client>}}``: ``file`` is the list the runner reads (``~`` is the
account's home), and ``refresh.ocx_client`` asks LINA's own running OCX to write it again
(``ocx integration client enable --client <client>``). A block without ``refresh`` has a list LINA only reads; one
without ``models`` has none.

``fingerprint`` is what the daily runner sync compares: OCX's model list and the runner's version; when either
changed since the last refresh, the list is refreshed on the hub and on each node that follows.
"""

from __future__ import annotations

import hashlib
import json
import subprocess
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx
from hermes_cli.lina_runners import RunnerPin


def list_path(runner: RunnerPin) -> Path | None:
    found = runner.models.get("file") if isinstance(runner.models, dict) else None
    return Path(found).expanduser() if isinstance(found, str) and found else None


def ocx_client(runner: RunnerPin) -> str | None:
    refresh = runner.models.get("refresh") if isinstance(runner.models, dict) else None
    client = refresh.get("ocx_client") if isinstance(refresh, dict) else None
    return client if isinstance(client, str) and client else None


def refresh(root: Path, runner: RunnerPin,
            run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> str | None:
    """Ask LINA's own OCX to write *runner*'s model list again; None when it did (or there is nothing to refresh),
    else why not in one line."""
    client = ocx_client(runner)
    if client is None:
        return None
    version = lina_ocx.current_version(root)
    if version is None:
        return "LINA's OCX is not installed: run `lina models install`"
    if not lina_ocx.own_proxy_running(root, lina_ocx.configured_port(root)):
        return "LINA's OCX is not running: run `lina services start ocx`"
    target = list_path(runner)
    if target is not None:
        target.parent.mkdir(parents=True, exist_ok=True)  # OCX reports a client absent until its folder exists
    result = run(lina_ocx.ocx_argv(root, version, "integration", "client", "enable", "--client", client),
                 env=lina_ocx.ocx_env(root), capture_output=True, text=True, encoding="utf-8", errors="replace",
                 timeout=120, check=False)
    if result.returncode != 0:
        tail = ((result.stderr or "") + (result.stdout or "")).strip().splitlines()[-1:] or ["no output"]
        return f"`ocx integration client enable --client {client}` failed: {tail[0]}"
    return None


def fingerprint(ocx_models, version: str | None) -> str:
    text = json.dumps({"ocx": ocx_models, "version": version}, sort_keys=True, ensure_ascii=False, default=str)
    return hashlib.sha256(text.encode()).hexdigest()


def read_ocx_models(root: Path):
    from hermes_cli import lina_models
    return lina_models.run(("models", "live", "--json"), root)
