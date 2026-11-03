"""Read-only board routes (lina#39): the parts of today's board snapshot lina#112 does not serve, as JSON for the client
API (schema: ``docs/api/board-client.yaml``).

``GET /api/lina/board`` is one snapshot; ``/api/lina/work/due``, ``/api/lina/devices`` and ``/api/lina/quota`` are its
parts for polling them on their own (the device probe and the OCX read are the slow ones). ``/api/lina/work/items``
and ``/api/lina/work/items/{id}`` (lina#166) serve the goals and to-dos with their comments, runs and links;
``/api/lina/work/runs/{id}/record`` (lina#201) one worker run's record, round by round. Like ``lina_models.py``
every response carries an ``ETag`` and a matching ``If-None-Match`` answers 304; the tag leaves out the snapshot time
``at``, so it changes only when the data does. Mounted through ``lina_models.router``, so the dashboard's auth gate
applies the same way.
"""

import hashlib
import json
from typing import Callable, Optional

from fastapi import APIRouter, Header, HTTPException, Request, Response, WebSocket
from starlette.concurrency import run_in_threadpool

router = APIRouter()


async def _serve(read: Callable[[], dict], if_none_match: Optional[str]) -> Response:
    data = await run_in_threadpool(read)
    tagged = json.dumps({k: v for k, v in data.items() if k != "at"}, sort_keys=True, separators=(",", ":"))
    etag = '"' + hashlib.sha256(tagged.encode()).hexdigest()[:32] + '"'
    headers = {"ETag": etag, "Cache-Control": "no-cache"}
    if if_none_match == etag:
        return Response(status_code=304, headers=headers)
    return Response(content=json.dumps(data, ensure_ascii=False), media_type="application/json", headers=headers)


@router.get("/api/lina/board")
async def lina_board(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_board import board
    return await _serve(board, if_none_match)


@router.get("/api/lina/work/due")
async def lina_work_due(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_board import due
    return await _serve(due, if_none_match)


@router.get("/api/lina/work/items")
async def lina_work_items(agent: Optional[str] = None, if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_board_items import items
    return await _serve(lambda: items(agent), if_none_match)


@router.get("/api/lina/work/items/{item_id}")
async def lina_work_item(item_id: str, if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_board_items import ItemNotFound, RecordUnreadable, item
    try:
        return await _serve(lambda: item(item_id), if_none_match)
    except ItemNotFound as exc:
        raise HTTPException(status_code=404, detail=f"no goal or to-do {item_id}") from exc
    except RecordUnreadable as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc


@router.get("/api/lina/work/runs/{run_id}/record")
async def lina_work_run_record(run_id: str, if_none_match: Optional[str] = Header(default=None)):
    import time

    from hermes_cli.lina_ocx import lina_root
    from hermes_cli.worker import settings
    from hermes_cli.worker.record import RunNotFound, record
    try:
        return await _serve(lambda: record(settings.root(lina_root()), run_id, time.time()), if_none_match)
    except RunNotFound as exc:
        raise HTTPException(status_code=404, detail=f"no worker run {run_id}") from exc


async def _act(request: Request, write: Callable[[object], dict]) -> dict:
    """An owner's write (lina#149): rate-limited per client before the body is read; a refusal is a 4xx with one
    line and writes nothing."""
    from hermes_cli.lina_board_write import Refused, allow_write
    if not allow_write(request.client.host if request.client else ""):
        raise HTTPException(status_code=429, detail="too many writes: 30 a minute per client")
    raw = await request.body()
    try:
        body = json.loads(raw) if raw.strip() else None
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="the body is not JSON") from exc
    try:
        return await run_in_threadpool(write, body)
    except Refused as exc:
        raise HTTPException(status_code=exc.status, detail=exc.detail) from exc


@router.post("/api/lina/work/items/{item_id}/done")
async def lina_work_item_done(item_id: str, request: Request):
    from hermes_cli.lina_board_write import mark_done
    return await _act(request, lambda body: mark_done(item_id, body))


@router.post("/api/lina/work/items/{item_id}/undo")
async def lina_work_item_undo(item_id: str, request: Request):
    from hermes_cli.lina_board_write import undo_done
    return await _act(request, lambda body: undo_done(item_id, body))


@router.post("/api/lina/work/items/{item_id}/comments")
async def lina_work_item_comment(item_id: str, request: Request):
    from hermes_cli.lina_board_write import comment
    return await _act(request, lambda body: comment(item_id, body))


@router.post("/api/lina/work/items/{item_id}/retry")
async def lina_work_item_retry(item_id: str, request: Request):
    from hermes_cli.lina_board_write import retry
    return await _act(request, lambda body: retry(item_id, body))


@router.post("/api/lina/work/add")
async def lina_work_add(request: Request):
    from hermes_cli.lina_board_write import quick_add
    return await _act(request, quick_add)


@router.post("/api/lina/notices/read")
async def lina_notices_read(request: Request):
    from hermes_cli.lina_board_write import mark_read
    return await _act(request, mark_read)


@router.websocket("/api/lina/events")
async def lina_events(ws: WebSocket):
    from hermes_cli.lina_board_events import serve
    await serve(ws)


@router.get("/api/lina/devices")
async def lina_devices(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_board import devices
    return await _serve(devices, if_none_match)


@router.get("/api/lina/quota")
async def lina_quota(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_board import quota
    return await _serve(quota, if_none_match)
