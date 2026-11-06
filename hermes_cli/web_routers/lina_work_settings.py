"""The work settings over the client API (lina#256, schema ``docs/api/settings-client.yaml``).

``GET /api/lina/work/settings`` serves the settings with the revision's ETag (a matching ``If-None-Match`` answers
304); ``PATCH`` applies a JSON Merge Patch and needs ``If-Match`` (428 without, 412 when stale, 422 with
``{detail, errors: [{path, message}]}`` when invalid). ``…/history``, ``…/history/{rev}``, ``POST …/rollback``,
``GET …/models`` and ``POST …/route`` complete it. Writes pass the owner-device gate
(``hermes_cli/lina_work_settings_gate.py``) on top of the dashboard's auth gate; reads never ask tailscale except for
the ``writable`` flag. Mounted through ``lina_models.router`` like ``lina_board``.
"""

import ipaddress
import json
import time
from typing import Callable, Optional

from fastapi import APIRouter, Header, HTTPException, Request, Response
from fastapi.responses import JSONResponse
from starlette.concurrency import run_in_threadpool

router = APIRouter()
BASE = "/api/lina/work/settings"


def _store():
    from hermes_cli.lina_work_settings_store import Store
    return Store()  # the work settings' own root (lina_work_settings.base), the one the worker reads


def _base():
    from hermes_cli.lina_work_settings import base
    return base()


async def _current():
    """The settings now; 503 with the validator's fixed line while config.yaml cannot be read as a mapping."""
    from hermes_cli.lina_work_settings import SettingsError
    try:
        return await run_in_threadpool(lambda: _store().current())
    except SettingsError as exc:
        raise HTTPException(status_code=503, detail=_invalid(exc.errors)["detail"]) from exc


LOOPBACK = ("127.0.0.1", "::1")


def _peer(request: Request) -> str:
    """The address the gate asks tailscale about. Behind the hub's own ``tailscale serve`` the direct peer is
    loopback and the device is the last ``X-Forwarded-For`` entry (tailscale serve sets it and drops what the client
    sent); a loopback caller is already on the hub, where the CLI writes anyway (lina#283). Any other direct peer's
    header is ignored, and a malformed entry leaves the direct peer."""
    host = request.client.host if request.client else ""
    forwarded = request.headers.get("x-forwarded-for")
    if host not in LOOPBACK or not forwarded:
        return host
    last = forwarded.split(",")[-1].strip()
    try:
        return str(ipaddress.ip_address(last))
    except ValueError:
        return host


def _revision(rev) -> dict:
    audit = rev.audit or {}
    return {"id": rev.id, "at": audit.get("at"), "by": audit.get("by"), "base": audit.get("base")}


def _json(data: dict, etag: str | None = None, status: int = 200) -> Response:
    headers = {"Cache-Control": "no-cache", **({"ETag": etag} if etag else {})}
    return Response(content=json.dumps(data, ensure_ascii=False), status_code=status, media_type="application/json",
                    headers=headers)


def _version() -> int:
    from hermes_cli.lina_board import API_VERSION
    return API_VERSION


@router.get(BASE)
async def get_settings(request: Request, if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_work_settings import validate
    from hermes_cli.lina_work_settings_gate import GATE
    rev = await _current()
    if if_none_match == rev.etag:
        return Response(status_code=304, headers={"ETag": rev.etag, "Cache-Control": "no-cache"})
    writable = await run_in_threadpool(GATE.writable, _peer(request), _base())
    return _json({"version": _version(), "at": time.time(), "revision": _revision(rev), "settings": rev.doc,
                  "writable": writable, "warnings": validate(rev.doc), "errors": []}, rev.etag)


async def _write(request: Request, if_match: Optional[str], change: Callable[[object, dict, str], tuple]) -> Response:
    from hermes_cli.lina_work_settings import SettingsError
    from hermes_cli.lina_work_settings_gate import GATE
    from hermes_cli.lina_work_settings_store import Stale
    by, status, reason = await run_in_threadpool(GATE.check, _peer(request), _base())
    if by is None:
        raise HTTPException(status_code=status, detail=reason)
    if not if_match:
        raise HTTPException(status_code=428, detail="If-Match is required: send the ETag of the settings you read")
    raw = await request.body()
    try:
        body = json.loads(raw) if raw.strip() else None
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="the body is not JSON") from exc
    try:
        rev, changes = await run_in_threadpool(change, body, by, if_match.removeprefix("W/"))
    except Stale as exc:
        current = exc.current
        return _json({"detail": f"the settings changed since you read them (now r{int(current.n)}): read again"},
                     current.etag, 412)
    except SettingsError as exc:
        return JSONResponse(status_code=422, content=_invalid(exc.errors))
    return _json({"ok": True, "revision": _revision(rev), "changes": changes}, rev.etag)


def _invalid(errors: list[dict]) -> dict:
    """The 422 body, made only of the validator's own ``{path, message}`` strings (never exception text)."""
    rows = [{"path": str(e.get("path", "")), "message": str(e.get("message", ""))} for e in errors] or [
        {"path": "", "message": "refused"}]
    more = f" (+{len(rows) - 1} more)" if len(rows) > 1 else ""
    where = f"{rows[0]['path']}: " if rows[0]["path"] else ""
    return {"detail": f"{where}{rows[0]['message']}{more}", "errors": rows}


def _patch(body, by: dict, expect: str):
    from hermes_cli.lina_work_settings import SettingsError, merge_patch
    if not isinstance(body, dict):
        raise SettingsError([{"path": "", "message": "the body is a JSON Merge Patch object"}])
    return _store().apply(lambda doc: merge_patch(doc, body), by, expect=expect)


@router.patch(BASE)
async def patch_settings(request: Request, if_match: Optional[str] = Header(default=None)):
    return await _write(request, if_match, _patch)


@router.post(f"{BASE}/rollback")
async def rollback_settings(request: Request, if_match: Optional[str] = Header(default=None)):
    return await _write(request, if_match, lambda _body, by, expect: _store().rollback(by, expect=expect))


@router.get(f"{BASE}/history")
async def settings_history(limit: int = 20):
    lines = await run_in_threadpool(lambda: _store().history(max(1, min(limit, 500))))
    return _json({"version": _version(), "at": time.time(), "revisions": lines})


@router.get(f"{BASE}/history/{{rev}}")
async def settings_revision(rev: str):
    from hermes_cli.lina_work_settings_store import rev_number
    n = rev_number(rev)
    try:
        found = await run_in_threadpool(lambda: _store().revision(n)) if n is not None else None
    except KeyError:
        found = None
    if found is None:
        raise HTTPException(status_code=404, detail=f"no settings revision {rev}")
    return _json({"version": _version(), "at": time.time(), "revision": _revision(found), "settings": found.doc},
                 found.etag)


@router.get(f"{BASE}/models")
async def settings_models(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_work_settings_models import view
    from hermes_cli.web_routers.lina_board import _serve
    from hermes_cli.lina_work_settings import SettingsError

    def read() -> dict:
        try:
            data = view(_base())
        except SettingsError as exc:
            raise HTTPException(status_code=503, detail=_invalid(exc.errors)["detail"]) from exc
        # OCX's own error text stays in the CLI and the log; the API names only the source that failed
        data["errors"] = [{"source": str(e.get("source")), "message": "could not be read; the list is the last good one"
                           if data["models"] else "could not be read"} for e in data["errors"]]
        return {"version": _version(), "at": time.time(), **data}
    return await _serve(read, if_none_match)


@router.post(f"{BASE}/route")
async def settings_route(request: Request):
    from hermes_cli.lina_work_settings_route import route
    try:
        body = json.loads(await request.body() or b"{}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="the body is not JSON") from exc
    if isinstance(body, dict):
        labels = [] if body.get("labels") is None else body["labels"]  # absent or null: no labels; false, 0, "", {}: 400
        size = body.get("size")
    else:
        labels, size = None, None
    if not isinstance(labels, list) or not all(isinstance(x, str) for x in labels) or size not in (None, "S", "M", "L"):
        raise HTTPException(status_code=400, detail='the body is {"labels": [text], "size": "S"|"M"|"L"|null}')
    doc = (await _current()).doc
    return _json({"version": _version(), "at": time.time(), **route(doc, labels, size)})
