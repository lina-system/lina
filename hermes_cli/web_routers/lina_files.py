"""Browse registered forge repositories and save immutable file revisions from the app."""

from __future__ import annotations

import base64
import binascii
from collections.abc import Callable
from typing import Annotated, Literal, ParamSpec, TypeVar

from fastapi import APIRouter, Depends, HTTPException, Query, Response
from fastapi.responses import PlainTextResponse
from pydantic import BaseModel, ConfigDict

from hermes_cli.work import env, forge_catalog, forge_files, forge_preview

router = APIRouter(prefix="/api/lina/files", tags=["Forge files"])
_P = ParamSpec("_P")
_T = TypeVar("_T")


def _call(fn: Callable[_P, _T], *args: _P.args, **kwargs: _P.kwargs) -> _T:
    try:
        return fn(*args, **kwargs)
    except (forge_catalog.CatalogError, forge_files.ForgeError) as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc


def _remote(area: str, repo: str) -> str:
    return _call(forge_catalog.remote, area, repo)


Remote = Annotated[str, Depends(_remote)]


@router.get("/areas")
def areas():
    return _call(forge_catalog.areas)


@router.get("/areas/{area}/repos")
def repos(area: str):
    return _call(forge_catalog.repos, area)


@router.get("/inbox")
def inbox():
    return _call(forge_catalog.inbox)


@router.get("/artifacts")
def work_artifacts(item: str):
    return _call(forge_catalog.for_work, item)


@router.get("/{area}/{repo}/context")
def file_context(area: str, repo: str, path: str):
    return _call(forge_catalog.context, area, repo, path)


@router.get("/{area}/{repo}/tree")
def tree(url: Remote, path: str = "", rev: str | None = None):
    return _call(forge_files.tree, url, path, rev)


@router.get("/{area}/{repo}/blob", response_class=Response)
def blob(url: Remote, path: str, rev: str | None = None):
    commit, content = _call(forge_files.blob, url, path, rev)
    return _file_response(commit, content)


def _file_response(commit: str, content: bytes, size: int | None = None) -> Response:
    headers = {
        "X-Git-Commit": commit,
        "X-Content-Type-Options": "nosniff",
        "Cache-Control": "private, no-cache",
    }
    status = 200
    if size is not None and len(content) < size:
        headers["Content-Range"] = f"bytes 0-{len(content) - 1}/{size}"
        status = 206
    return Response(content, status_code=status, media_type="application/octet-stream", headers=headers)


@router.get("/{area}/{repo}/preview", response_class=Response)
def preview(url: Remote, path: str, limit: Annotated[int, Query(ge=1, le=forge_preview.MAX_PREVIEW_BYTES)]):
    """Read a bounded prefix of the file as exported by Git at HEAD."""
    return _file_response(*_call(forge_preview.preview, url, path, limit))


@router.get("/{area}/{repo}/history")
def history(url: Remote, path: str):
    return _call(forge_files.history, url, path)


@router.get("/{area}/{repo}/diff", response_class=PlainTextResponse)
def diff(url: Remote, path: str, old: str, new: str):
    return PlainTextResponse(_call(forge_files.diff, url, path, old, new))


class FileEdit(BaseModel):
    model_config = ConfigDict(extra="forbid")
    path: str
    base: str | None


class Save(FileEdit):
    content: str
    encoding: Literal["utf-8", "base64"] = "utf-8"


class Restore(FileEdit):
    source: str


@router.post("/{area}/{repo}/save")
def save(url: Remote, edit: Save):
    try:
        content = (base64.b64decode(edit.content, validate=True) if edit.encoding == "base64"
                   else edit.content.encode("utf-8"))
    except (ValueError, binascii.Error) as exc:
        raise HTTPException(status_code=422, detail="Content does not match its encoding.") from exc
    # Interactive dashboard sessions represent the work owner on every device.
    return _call(forge_files.save, url, edit.path, edit.base, content, env.load().owner_name)


@router.post("/{area}/{repo}/restore")
def restore(url: Remote, edit: Restore):
    return _call(forge_files.restore, url, edit.path, edit.base, edit.source, env.load().owner_name)


class ArtifactLocation(BaseModel):
    model_config = ConfigDict(extra="forbid")
    path: str
    commit: str


@router.post("/{area}/{repo}/artifacts/{artifact_id}")
def bind_artifact(area: str, repo: str, url: Remote, artifact_id: int, location: ArtifactLocation):
    """Attach a published file to an existing run result at its recorded path."""
    parent, _, name = location.path.rpartition("/")
    listing = _call(forge_files.tree, url, parent, location.commit)
    if not any(entry["name"] == name and entry["type"] == "file" for entry in listing["entries"]):
        raise HTTPException(status_code=404, detail="No regular file at that revision and path.")
    return _call(forge_catalog.bind_artifact, area, repo, artifact_id, location.path, listing["commit"])
