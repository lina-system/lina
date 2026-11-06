"""``GET /api/lina/status/checks`` (lina#233): the hub's own health checks from ``<LINA root>/status/*.json``, newest
first (schema: ``docs/api/board-client.yaml``). The read lives in ``hermes_cli/lina_status_checks.py``; a malformed
file is one error row, never a failed response. Mounted through ``lina_board.router`` like the other board routes, so
the dashboard's auth gate applies the same way and the ``ETag``/``If-None-Match`` pattern is the board's ``_serve``.
"""

from typing import Optional

from fastapi import APIRouter, Header, Response

router = APIRouter()

@router.get("/api/lina/status/checks")
async def lina_status_checks(if_none_match: Optional[str] = Header(default=None)) -> Response:
    from hermes_cli.lina_status_checks import checks
    from hermes_cli.web_routers.lina_board import _serve
    return await _serve(checks, if_none_match)
