"""Forge Files HTTP contracts over a real work store and a local Git transport."""

from __future__ import annotations

import base64
from collections.abc import Iterator
from contextlib import closing
from dataclasses import dataclass
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
from typing import Final

from fastapi import FastAPI
from fastapi.testclient import TestClient
import pytest

from tests.hermes_cli._work_env import WorkEnv
from tests.hermes_cli import work_pg_fixture

pg_db = work_pg_fixture.pg_db

FILES: Final = "/api/lina/files"
REPO: Final = "A1/album"
ALBUM: Final = f"{FILES}/{REPO}"
NOTE: Final = "docs/note.txt"
ORIGINAL: Final = b"first version\n"
CURRENT: Final = b"second version\n"
SEED_AUTHOR: Final = "Fixture Writer"
OWNER_NAME: Final = "작업 책임자"
MEDIA_SIZE: Final = 2**20


def _git(cwd: Path, *args: str) -> bytes:
    result = subprocess.run(
        ["git", "-C", str(cwd), *args], capture_output=True, check=True, timeout=30,
    )
    return result.stdout


@pytest.fixture
def work(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, request: pytest.FixtureRequest) -> WorkEnv:
    from hermes_cli import lina_deps

    forge = tmp_path / "forge"
    create = ["git", "init", "--bare", "--initial-branch=main", str(forge / "{repo}.git")]
    extra = f"  forge:\n    url: '{forge.as_uri()}/{{repo}}.git'\n    create_command: {json.dumps(create)}\n"
    if getattr(request, "param", None) == "postgres":
        pg = request.getfixturevalue("pg_db")
        work_pg_fixture.reset_work_schema(pg)
        extra += f"  store: {{backend: postgres, psql: {json.dumps(pg.argv)}}}\n"
    work = WorkEnv(tmp_path, monkeypatch, extra=extra)
    monkeypatch.setenv("LINA_HOME", str(work.root))
    monkeypatch.setenv("GIT_CONFIG_NOSYSTEM", "1")
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", str(work.home / ".gitconfig"))
    monkeypatch.setenv("XDG_CONFIG_HOME", str(work.home / ".config"))
    monkeypatch.setattr(lina_deps, "lina_root", lambda: work.root)
    work.goal_and_todos()
    return work


@pytest.fixture
def client(work: WorkEnv) -> Iterator[TestClient]:
    from hermes_cli.web_routers import lina_models

    app = FastAPI()
    app.include_router(lina_models.router)
    with TestClient(app) as client:
        yield client


@pytest.fixture
def remote(work: WorkEnv) -> Path:
    from hermes_cli.work import repos

    remote = work.tmp / "forge" / f"{REPO}.git"
    assert repos.create(REPO)["forge_url"] == remote.as_uri()
    _git(remote, "config", "uploadpack.allowFilter", "true")
    _git(remote, "config", "uploadpack.allowReachableSHA1InWant", "true")
    return remote


@dataclass(frozen=True, slots=True)
class History:
    old: str
    tip: str


@pytest.fixture
def history(remote: Path, tmp_path: Path) -> History:
    seed = tmp_path / "seed"
    seed.mkdir()
    _git(seed, "init", "--initial-branch=main")
    _git(seed, "config", "user.name", SEED_AUTHOR)
    _git(seed, "config", "user.email", "fixture@example.invalid")
    (seed / "docs/subfolder").mkdir(parents=True)
    (seed / "docs/subfolder/readme").write_bytes(b"nested\n")
    (seed / "media.bin").write_bytes(hashlib.shake_256(b"forge-media-fixture").digest(MEDIA_SIZE))
    note = seed / NOTE
    note.write_bytes(ORIGINAL)
    _git(seed, "add", ".")
    _git(seed, "commit", "-m", "Add note and media")
    old = _git(seed, "rev-parse", "HEAD").decode().strip()
    note.write_bytes(CURRENT)
    _git(seed, "commit", "-am", "Revise note")
    tip = _git(seed, "rev-parse", "HEAD").decode().strip()
    _git(seed, "push", remote.as_uri(), "main")
    return History(old, tip)


def test_lists_work_areas(client: TestClient) -> None:
    response = client.get(f"{FILES}/areas")
    assert response.status_code == 200, response.text
    assert [(row["id"], row["title"]) for row in response.json()["areas"]] == [("A1", "Dev")]


def test_sqlite_file_context_reports_brain_unavailable(client: TestClient, remote: Path) -> None:
    response = client.get(f"{ALBUM}/context", params={"path": NOTE})
    assert response.status_code == 200, response.text
    assert response.json()["brain"] == {"available": False, "pages": []}


@pytest.mark.parametrize(("path", "expected"), [
    ("/areas/A1/repos", [(REPO, "album")]),
    ("/inbox", []),
])
def test_lists_only_repos_in_the_requested_location(
    client: TestClient, remote: Path, path: str, expected: list[tuple[str, str]],
) -> None:
    response = client.get(FILES + path)
    assert response.status_code == 200, response.text
    assert [(row["id"], row["name"]) for row in response.json()["repos"]] == expected


def test_lists_tree_entries_at_the_tip(client: TestClient, history: History) -> None:
    response = client.get(f"{ALBUM}/tree", params={"path": "docs"})
    assert response.status_code == 200, response.text
    body = response.json()
    assert body["commit"] == history.tip
    assert sorted((entry["name"], entry["type"]) for entry in body["entries"]) == [
        ("note.txt", "file"),
        ("subfolder", "folder"),
    ]


def test_reads_a_small_blob_without_transferring_unrelated_media(
    client: TestClient, history: History, remote: Path,
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    # The pack trace records incoming bytes even if the receiver throws its clone away.
    full_trace = tmp_path / "full.pack"
    monkeypatch.setenv("GIT_TRACE_PACKFILE", str(full_trace))
    _git(tmp_path, "clone", "--no-checkout", remote.as_uri(), "full-transfer")
    assert full_trace.stat().st_size >= MEDIA_SIZE

    trace = tmp_path / "blob.pack"
    monkeypatch.setenv("GIT_TRACE_PACKFILE", str(trace))
    response = client.get(f"{ALBUM}/blob", params={"path": NOTE})
    assert response.status_code == 200, response.text
    assert response.headers["X-Git-Commit"] == history.tip
    assert response.content == CURRENT
    transferred = trace.stat().st_size
    assert 0 < transferred < MEDIA_SIZE // 2, f"received {transferred} Git pack bytes"


def test_reads_the_blob_at_an_explicit_revision(client: TestClient, history: History) -> None:
    response = client.get(f"{ALBUM}/blob", params={"path": NOTE, "rev": history.old})
    assert response.status_code == 200, response.text
    assert response.headers["X-Git-Commit"] == history.old
    assert response.content == ORIGINAL


@pytest.mark.platforms("posix")
def test_preview_returns_prefix_size_and_revision(client: TestClient, history: History) -> None:
    response = client.get(f"{ALBUM}/preview", params={"path": "media.bin", "limit": 73})
    assert response.status_code == 206, response.text
    assert response.headers["X-Git-Commit"] == history.tip
    assert response.headers["Content-Range"] == f"bytes 0-72/{MEDIA_SIZE}"
    assert response.content == hashlib.shake_256(b"forge-media-fixture").digest(73)


def test_history_reports_path_commits_with_their_authors(client: TestClient, history: History) -> None:
    response = client.get(f"{ALBUM}/history", params={"path": NOTE})
    assert response.status_code == 200, response.text
    assert [(row["commit"], row["author"]) for row in response.json()["commits"]] == [
        (history.tip, SEED_AUTHOR),
        (history.old, SEED_AUTHOR),
    ]


def test_diff_returns_plain_unified_diff(client: TestClient, history: History) -> None:
    response = client.get(f"{ALBUM}/diff", params={"path": NOTE, "old": history.old, "new": history.tip})
    assert response.status_code == 200, response.text
    assert response.headers["Content-Type"].split(";", 1)[0] == "text/plain"
    lines = response.text.splitlines()
    assert f"--- a/{NOTE}" in lines
    assert f"+++ b/{NOTE}" in lines
    assert f"-{ORIGINAL.decode().strip()}" in lines
    assert f"+{CURRENT.decode().strip()}" in lines


def test_save_pushes_utf8_as_the_configured_work_human(
    client: TestClient, remote: Path, history: History, work: WorkEnv,
) -> None:
    config = work.root / "config.yaml"
    text = config.read_text(encoding="utf-8")
    config.write_text(text.replace("name: 주인", f"name: {OWNER_NAME}"), encoding="utf-8")

    content = "새 버전\n"
    payload = {"path": NOTE, "base": history.tip, "content": content}
    forged = client.post(f"{ALBUM}/save", json={**payload, "author": "Another Writer"})
    if forged.status_code in (400, 422):
        response = client.post(f"{ALBUM}/save", json=payload)
    else:
        response = forged
    assert response.status_code == 200, response.text
    commit = response.json()["commit"]
    assert _git(remote, "rev-parse", "HEAD").decode().strip() == commit
    assert _git(remote, "rev-parse", f"{commit}^").decode().strip() == history.tip
    assert _git(remote, "show", f"{commit}:{NOTE}") == content.encode("utf-8")
    assert _git(remote, "show", "-s", "--format=%an", commit).decode().strip() == OWNER_NAME


@pytest.mark.parametrize("use_stale_base", [True, False], ids=["stale", "null"])
def test_save_rejects_a_base_other_than_the_current_tip(
    client: TestClient, remote: Path, history: History, use_stale_base: bool,
) -> None:
    response = client.post(f"{ALBUM}/save", json={
        "path": NOTE, "base": history.old if use_stale_base else None, "content": "conflicting write",
    })
    assert response.status_code == 409, response.text
    assert _git(remote, "rev-parse", "HEAD").decode().strip() == history.tip
    assert _git(remote, "show", f"HEAD:{NOTE}") == CURRENT


def test_first_save_pushes_a_root_commit_with_binary_content(client: TestClient, remote: Path) -> None:
    contents = b"\x00\xff\r\n"
    response = client.post(f"{ALBUM}/save", json={
        "path": "data.bin", "base": None,
        "content": base64.b64encode(contents).decode("ascii"), "encoding": "base64",
    })
    assert response.status_code == 200, response.text
    commit = response.json()["commit"]
    assert _git(remote, "rev-list", "--parents", "HEAD").decode().splitlines() == [commit]
    assert _git(remote, "show", f"{commit}:data.bin") == contents


def test_restore_pushes_a_new_commit_with_the_old_file(client: TestClient, remote: Path, history: History) -> None:
    response = client.post(f"{ALBUM}/restore", json={"path": NOTE, "base": history.tip, "source": history.old})
    assert response.status_code == 200, response.text
    commit = response.json()["commit"]
    assert _git(remote, "rev-parse", "HEAD").decode().strip() == commit
    assert _git(remote, "rev-parse", f"{commit}^").decode().strip() == history.tip
    assert _git(remote, "show", f"{commit}:{NOTE}") == ORIGINAL


@pytest.fixture
def artifact(work: WorkEnv, history: History) -> dict[str, str]:
    record = {
        "run": "fixture-run", "task": "T1", "goal": "G1", "repo": REPO,
        "path": NOTE, "commit": history.tip, "kind": "draft",
    }
    created_at = "2027-01-15T07:00:00Z"
    with closing(sqlite3.connect(work.work / "work.db")) as db, db:
        db.execute("PRAGMA foreign_keys = ON")
        db.execute(
            "INSERT INTO run (id, runner, title, agent_id, goal_id, created_at) VALUES (?, ?, ?, ?, ?, ?)",
            (record["run"], "omo", "Fixture run", "alpha", record["goal"], created_at),
        )
        db.execute(
            "INSERT INTO run_todo (run_id, todo_id, closes, added_by, added_at) VALUES (?, ?, 0, 'alpha', ?)",
            (record["run"], record["task"], created_at),
        )
        db.execute(
            "INSERT INTO artifact (run_id, todo_id, goal_id, repo, path, commit_sha, kind, created_at, agent_id) "
            "VALUES (:run, :task, :goal, :repo, :path, :commit, :kind, :created_at, 'alpha')",
            {**record, "created_at": created_at},
        )
    return record


@pytest.mark.parametrize(("url", "query", "matching", "unrelated"), [
    (f"{ALBUM}/context", "path", NOTE, "docs/other.txt"),
    (f"{FILES}/artifacts", "item", "T1", "T9"),
])
def test_finds_persisted_artifacts_by_repo_path_or_work_item(
    client: TestClient, artifact: dict[str, str], url: str, query: str, matching: str, unrelated: str,
) -> None:
    response = client.get(url, params={query: matching})
    assert response.status_code == 200, response.text
    (row,) = response.json()["artifacts"]
    assert {key: row[key] for key in artifact} == artifact

    other = client.get(url, params={query: unrelated})
    assert other.status_code == 200, other.text
    assert other.json()["artifacts"] == []


@pytest.mark.parametrize("work", ["sqlite", "postgres"], indirect=True)
def test_publish_and_accept_a_run_result(client: TestClient, work: WorkEnv, remote: Path) -> None:
    from hermes_cli.work import artifacts, ledger, store

    run = "fixture-run"
    at = "2024-01-01T00:00:00Z"
    output = work.tmp / "out"
    output.mkdir()
    (output / "note.txt").write_bytes(CURRENT)
    with closing(ledger.open_db()) as db, store.transaction(db):
        db.execute("INSERT INTO run(id,runner,title,agent_id,goal_id,created_at) "
                   "VALUES (?, 'local', 'Result', 'alpha', 'G1', ?)", (run, at))
        db.execute("INSERT INTO run_todo(run_id,todo_id,closes,added_by,added_at) "
                   "VALUES (?, 'T1', FALSE, 'alpha', ?)", (run, at))
        (draft,) = artifacts.record(db, run, output, work.desk, at)

    path = draft["path"]
    assert draft["repo"] is None and draft["commit"] is None
    saved = client.post(f"{ALBUM}/save", json={"path": path, "content": CURRENT.decode(), "base": None})
    assert saved.status_code == 200, saved.text
    location = {"path": path, "commit": saved.json()["commit"]}
    url = f"{ALBUM}/artifacts/{draft['id']}"
    expected = {**draft, **location, "repo": REPO}
    for _ in range(2):
        bound = client.post(url, json=location)
        assert bound.status_code == 200, bound.text
        assert bound.json()["artifact"] == expected
    work.ok("artifact", "final", str(draft["id"]))
    expected["kind"] = "final"
    assert client.get(f"{ALBUM}/context", params={"path": path}).json()["artifacts"] == [expected]
    for item in (run, "T1", "G1"):
        assert client.get(f"{FILES}/artifacts", params={"item": item}).json()["artifacts"] == [expected]
