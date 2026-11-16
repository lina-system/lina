"""Reports for the client API (lina#263, schema ``docs/api/work-client.yaml``): ``GET /api/lina/reports/{id}``,
``GET /api/lina/reports?card=&goal=`` and the report's page, ``GET /api/lina/reports/{id}/page``, plain HTML for a
browser opening the link a chat message carries. Mounted through ``lina_models.router`` like ``lina_board``, so
the dashboard's auth gate (the owner's sign-in) is in front of every route; the change event of an update is the
``reports`` source of ``GET /api/lina/events`` (``hermes_cli/lina_board_events.py``).

The store is read as its owner: the owner sees every agent's reports. A private report is served only to the
owner's own session: a request admitted by a bearer token of another principal (``token_authenticated``) gets 404
for it and a list without it.
"""

from __future__ import annotations

from contextlib import closing
from typing import Optional

from fastapi import APIRouter, HTTPException, Query, Request
from fastapi.responses import HTMLResponse, JSONResponse
from starlette.concurrency import run_in_threadpool

router = APIRouter()


def _owner_session(request: Request) -> bool:
    return not getattr(request.state, "token_authenticated", False)


def summary_json(report) -> dict:
    return {"id": report.report_id, "title": report.title, "summary": report.summary, "agent": report.agent,
            "card": report.card, "goal": report.goal, "private": report.private, "version": report.version,
            "createdAt": report.created_at, "updatedAt": report.updated_at, "page": report.page_path()}


def _read(fn):
    from hermes_state_reports import owner_connection
    from hermes_state_store import StoreUnavailableError
    try:
        with closing(owner_connection()) as conn:
            return fn(conn)
    except StoreUnavailableError as exc:
        raise HTTPException(status_code=503, detail=str(exc)) from exc


async def _report(request: Request, report_id: str, version: Optional[int]):
    import hermes_state_reports as reports

    def read(conn):
        found = reports.get(conn, report_id, version)
        return found, (reports.versions(conn, report_id) if found else [])
    report, versions = await run_in_threadpool(_read, read)
    if report is None or (report.private and not _owner_session(request)):
        raise HTTPException(status_code=404, detail="no such report")
    return report, versions


@router.get("/api/lina/reports")
async def list_reports(request: Request, card: Optional[str] = Query(None), goal: Optional[str] = Query(None)):
    from hermes_state_reports import listing
    found = await run_in_threadpool(_read, lambda conn: listing(conn, card=card, goal=goal,
                                                                include_private=_owner_session(request)))
    return JSONResponse({"reports": [summary_json(report) for report in found]})


@router.get("/api/lina/reports/{report_id}")
async def get_report(request: Request, report_id: str, version: Optional[int] = Query(None, ge=1)):
    report, versions = await _report(request, report_id, version)
    return JSONResponse({**summary_json(report), "body": report.body, "shownVersion": version or report.version,
                         "versions": versions})


@router.get("/api/lina/reports/{report_id}/page")
async def report_page(request: Request, report_id: str):
    from hermes_cli.lina_reports_page import HEADERS, render
    report, _ = await _report(request, report_id, None)
    return HTMLResponse(render(report), headers=HEADERS)
