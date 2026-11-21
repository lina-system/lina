"""Provision an area repository through the configured forge and record its Git remote."""

from __future__ import annotations

from contextlib import closing
from contextvars import copy_context
import re
import subprocess
from typing import TypedDict

from hermes_cli.work import env


class RepoError(ValueError):
    """The repository, forge configuration or provisioning request is invalid."""


class Repo(TypedDict):
    id: str
    area_id: str
    name: str
    forge_url: str


def _forge(work: dict) -> tuple[str, list[str]]:
    forge = work.get("forge")
    if not isinstance(forge, dict):
        raise RepoError("Configure work.forge.url and work.forge.create_command first.")
    url, command = forge.get("url"), forge.get("create_command")
    if not isinstance(url, str) or "{repo}" not in url:
        raise RepoError("work.forge.url must include {repo}.")
    if not isinstance(command, list) or not command or not all(isinstance(arg, str) and arg for arg in command):
        raise RepoError("work.forge.create_command must be a nonempty argument list.")
    if not any("{repo}" in arg or "{url}" in arg for arg in command):
        raise RepoError("work.forge.create_command must identify the target with {repo} or {url}.")
    return url, command


def configured() -> bool:
    from hermes_cli.lina_deps import lina_root

    work = env._root_config(lina_root()).get("work")
    if not isinstance(work, dict):
        return False
    try:
        _forge(work)
    except RepoError:
        return False
    return True


def create(repo: str) -> Repo:
    """Use a fresh work binding for this invocation, including in a long-lived tool worker."""
    if not isinstance(repo, str) or not re.fullmatch(r"A[1-9][0-9]*/[a-z0-9][a-z0-9_-]{0,62}", repo):
        raise RepoError("Expected AREA/name (for example A1/album), with a lowercase repository slug.")
    return copy_context().run(_create, repo)


def _create(repo: str) -> Repo:
    from hermes_cli.work import ledger, store
    from hermes_cli.work.common import Lock

    template, command = _forge(env.bind(env.load()).section("work"))
    area, name = repo.split("/")
    url = template.replace("{repo}", repo)
    with Lock(), closing(ledger.open_db()) as db, store.transaction(db):
        if not store.exists(db, "area", area):
            raise RepoError(f"Unknown work area {area}.")
        # Reserve the key before invoking the hook. A concurrent creator waits for this transaction.
        reserved = db.execute(
            "INSERT INTO repo (id, area_id, name, forge_url) VALUES (?, ?, ?, ?) "
            "ON CONFLICT (id) DO NOTHING RETURNING id", (repo, area, name, url),
        ).fetchone()
        if reserved:
            argv = [arg.replace("{repo}", repo).replace("{url}", url) for arg in command]
            try:
                result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                        stderr=subprocess.DEVNULL, check=False, timeout=60)
            except subprocess.TimeoutExpired as exc:
                raise RepoError("Forge provisioning did not finish within 60 seconds.") from exc
            if result.returncode:
                raise RepoError(f"Forge provisioning failed (exit {result.returncode}).")
        else:
            (url,) = db.execute("SELECT forge_url FROM repo WHERE id = ?", (repo,)).fetchone()
    return Repo(id=repo, area_id=area, name=name, forge_url=url)
