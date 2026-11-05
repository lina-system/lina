"""Read-only LINA model routes (lina#32): what ``lina models`` prints, as JSON, for the client API.

``GET /api/lina/models``, ``/api/lina/models/accounts``, ``/status`` and ``/usage`` each run one ``ocx … --json`` of
LINA's own OCX (``hermes_cli/lina_models.py``). Changes are read by polling: every response carries an ``ETag`` of
its data and a matching ``If-None-Match`` answers 304. The quota view is CLI-only because it may probe upstream
providers on every read. Login, logout and account switching stay in the CLI.
"""

import hashlib
import json
from typing import Optional

from fastapi import APIRouter, Header, HTTPException, Response
from starlette.concurrency import run_in_threadpool

from hermes_cli.web_routers import lina_board, lina_push, lina_talk

router = APIRouter()
router.include_router(lina_board.router)  # lina#39: mounted here so web_server.py stays at its cap
router.include_router(lina_talk.router)  # lina#167: the same way
router.include_router(lina_push.router)  # lina#244: the same way


async def _serve(view: str, if_none_match: Optional[str]) -> Response:
    from hermes_cli.lina_models import query
    from hermes_cli.lina_ocx import OcxError
    try:
        data = await run_in_threadpool(query, view)
    except OcxError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc
    body = json.dumps({"view": view, "data": data}, sort_keys=True, separators=(",", ":"))
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


@router.get("/api/lina/models/usage")
async def lina_model_usage(if_none_match: Optional[str] = Header(default=None)):
    return await _serve("usage", if_none_match)
