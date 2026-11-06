"""Web Push routes (lina#244, schema: ``docs/api/push-client.yaml``): the public VAPID key, and adding or removing
this device's subscription. Mounted through ``lina_models.router`` like ``lina_board``, so the dashboard's auth gate
applies the same way; the writes share ``lina_board``'s per-client write limit and its 4xx refusals.
"""

from typing import Optional

from fastapi import APIRouter, Header, Request

from hermes_cli.web_routers.lina_board import _act, _serve

router = APIRouter()


def _root():
    from hermes_cli.lina_board import _root as root
    return root()


@router.get("/api/lina/push/key")
async def lina_push_key(if_none_match: Optional[str] = Header(default=None)):
    from hermes_cli.lina_push import key_view
    return await _serve(key_view, if_none_match)


@router.post("/api/lina/push/subscriptions")
async def lina_push_subscribe(request: Request):
    from hermes_cli.lina_push import save_subscription
    return await _act(request, lambda body: save_subscription(_root(), body))


@router.post("/api/lina/push/subscriptions/remove")
async def lina_push_unsubscribe(request: Request):
    from hermes_cli.lina_push import remove_subscription
    return await _act(request, lambda body: remove_subscription(_root(), body))
