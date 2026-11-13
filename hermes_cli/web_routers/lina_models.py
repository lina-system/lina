"""Read-only LINA model routes (lina#32): what ``lina models`` prints, as JSON, for the client API.

``GET /api/lina/models``, ``/api/lina/models/accounts``, ``/status`` and ``/usage`` each read this machine's magpie
(``hermes_cli/lina_models.py``). Changes are read by polling: every response carries an ``ETag`` of its data and a
matching ``If-None-Match`` answers 304. Account sign-in stays on magpie's own screen.

``GET /api/lina/usage?range=`` and ``/api/lina/models/state`` (lina#232, schema ``docs/api/board-client.yaml``) serve
usage by model, provider, account and caller key, and the models magpie lists, in the board's shape: ``version`` and
``at`` (left out of the ``ETag``). A magpie that does not answer is a 503.
"""

import hashlib
import json
from typing import Optional

from fastapi import APIRouter, Header, HTTPException, Query, Response
from starlette.concurrency import run_in_threadpool

from hermes_cli.web_routers import lina_board, lina_push, lina_talk

router = APIRouter()
router.include_router(lina_board.router)  # lina#39: mounted here so web_server.py stays at its cap
router.include_router(lina_talk.router)  # lina#167: the same way
router.include_router(lina_push.router)  # lina#244: the same way
router.include_router(__import__("hermes_cli.web_routers.lina_work_settings", fromlist=["router"]).router)  # lina#256


async def _serve(view: str, if_none_match: Optional[str]) -> Response:
    from hermes_cli.lina_magpie import MagpieError
    from hermes_cli.lina_models import query
    try:
        data = {"view": view, "data": await run_in_threadpool(query, view)}
    except MagpieError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc
    body = json.dumps(data, sort_keys=True, separators=(",", ":"))
    etag = '"' + hashlib.sha256(body.encode()).hexdigest()[:32] + '"'
    headers = {"ETag": etag, "Cache-Control": "no-cache"}
    if if_none_match == etag:
        return Response(status_code=304, headers=headers)
    return Response(content=body, media_type="application/json", headers=headers)


@router.get("/api/lina/models")
async def lina_models(if_none_match: Optional[str] = Header(default=None)):
    return await _serve("list", if_none_match)


@router.get("/api/lina/models/accounts")
async def lina_model_accounts(if_none_match: Optional[str] = Header(default=None)):
    return await _serve("accounts", if_none_match)


@router.get("/api/lina/models/status")
async def lina_model_status(if_none_match: Optional[str] = Header(default=None)):
    return await _serve("status", if_none_match)


async def _serve_view(read, if_none_match: Optional[str]) -> Response:
    import time

    from hermes_cli.lina_board import API_VERSION
    from hermes_cli.lina_magpie import MagpieError
    from hermes_cli.web_routers.lina_board import _serve as serve_board

    def snapshot() -> dict:
        return {"version": API_VERSION, "at": time.time(), **read()}
    try:
        return await serve_board(snapshot, if_none_match)
    except MagpieError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc


@router.get("/api/lina/usage")
async def lina_usage(range_: str = Query(default="today", alias="range", pattern="^(today|7d|30d)$"),
                     if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_models import usage
    return await _serve_view(lambda: usage(range_), if_none_match)


@router.get("/api/lina/models/state")
async def lina_models_state(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_models import models_state
    return await _serve_view(models_state, if_none_match)


@router.get("/api/lina/models/usage")
async def lina_model_usage(if_none_match: Optional[str] = Header(default=None)):
    return await _serve("usage", if_none_match)
