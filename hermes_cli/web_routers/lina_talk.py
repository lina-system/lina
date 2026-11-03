"""``POST /api/lina/work/items/{item_id}/talk`` (lina#167): the owner talks with the agent that owns a to-do; the
reply streams back as server-sent events (schema: ``docs/api/talk-client.yaml``). Mounted through
``lina_models.router`` like ``lina_board``, so the dashboard's auth gate applies the same way. The turn itself runs on
the agent's gateway (``hermes_cli/lina_talk.py``); bad input answers 4xx before anything reaches it.
"""

from fastapi import APIRouter, Body
from fastapi.responses import JSONResponse, Response, StreamingResponse
from starlette.concurrency import run_in_threadpool

router = APIRouter()


@router.post("/api/lina/work/items/{item_id}/talk")
async def lina_talk(item_id: str, body: dict = Body(...)):
    from hermes_cli import lina_talk as talk
    try:
        message = talk.check_message(body.get("message"))
        target = await run_in_threadpool(talk.resolve, item_id)
    except talk.TalkError as exc:
        return JSONResponse({"error": {"code": exc.code, "message": exc.message}}, status_code=exc.status)
    opened = await talk.open_talk(target, message)
    if opened.stream is None:
        return Response(content=opened.body, status_code=opened.status, media_type=opened.media_type)
    return StreamingResponse(opened.stream, media_type="text/event-stream",
                             headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})
