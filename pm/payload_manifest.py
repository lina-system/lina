"""Discover sealed payloads without repeatedly decoding adjacent manifests."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path


@dataclass(frozen=True, slots=True)
class PayloadManifest:
    repo: str
    venv: str
    store: str
    hermes_command: str
    store_python: str | None
    target: str


def _file_identity(info: os.stat_result) -> tuple[int, int, int, int, int]:
    return info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns, info.st_ctime_ns


@lru_cache(maxsize=8)
def _read_manifest(path: Path, identity: tuple[int, int, int, int, int]) -> PayloadManifest | None:
    # Compare the opened file on both sides of the read. A concurrent replacement
    # or edit must not cache bytes under the wrong identity.
    with path.open("rb") as stream:
        if _file_identity(os.fstat(stream.fileno())) != identity:
            raise OSError("payload manifest changed before reading")
        raw = stream.read()
        if _file_identity(os.fstat(stream.fileno())) != identity:
            raise OSError("payload manifest changed while reading")
    try:
        manifest = json.loads(raw.decode("utf-8-sig"))
    except ValueError:
        return None
    if not isinstance(manifest, dict) or not isinstance(manifest.get("repo"), str):
        return None
    runtime = manifest.get("runtime", {})
    commands = runtime.get("commands", {}) if isinstance(runtime, dict) else None
    if not isinstance(commands, dict):
        return None
    hermes_command = commands.get("hermes", "")
    venv = manifest.get("venv", "")
    store = manifest.get("store", "")
    if not all(isinstance(value, str) for value in (hermes_command, venv, store)):
        return None
    store_python = runtime.get("storePython")
    target = manifest.get("target")
    return PayloadManifest(manifest["repo"], venv, store, hermes_command,
                           store_python if isinstance(store_python, str) else None,
                           target if isinstance(target, str) else "")


def payload_manifest(root: Path) -> tuple[PayloadManifest, Path] | None:
    """Find the manifest beside the tree or on an in-place payload tree."""
    for path in (root.parent / "manifest.json", root / "manifest.json"):
        try:
            manifest = _read_manifest(path, _file_identity(path.stat()))
        except OSError:
            continue
        # Resolve on every lookup so a changed repo symlink takes effect even
        # when the manifest itself has stayed the same.
        if manifest is not None and (path.parent / manifest.repo).resolve() == root:
            return manifest, path.parent
    return None
