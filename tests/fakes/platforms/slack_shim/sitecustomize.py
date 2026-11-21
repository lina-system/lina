"""Point slack_sdk (and raw ``https://slack.com/api/`` aiohttp posts) at a local Slack stand-in.

slack_sdk has no base-URL environment variable, so this module is put on ``PYTHONPATH`` of the child
``hermes gateway run`` process by the contract harness. Python imports ``sitecustomize`` at startup;
``HERMES_STANDIN_SLACK_API`` must name the local stand-in. Every Web API client
the Slack adapter builds defaults its ``base_url`` to the stand-in, which also moves
``apps.connections.open`` and therefore the Socket Mode websocket. Nothing in Hermes is touched: the
redirect lives at the SDK/HTTP boundary, exactly where DNS would send the real traffic.

Side effect: when the variable is set, slack_sdk (and aiohttp) are imported eagerly at interpreter
startup, before Hermes runs, so the child pays that import up front even if Slack never connects.
"""

import os
import re

_API = os.environ.get("HERMES_STANDIN_SLACK_API", "")
if not re.fullmatch(r"http://127\.0\.0\.1:[1-9]\d*/api/?", _API):
    # Python reports ordinary sitecustomize exceptions and continues startup.
    raise SystemExit("Slack tests require a loopback stand-in")
_REAL_API = "https://slack.com/api/"


def _wrap_init(cls):
    original = cls.__init__
    if getattr(original, "_standin_wrapped", False):
        return

    def __init__(self, *args, **kwargs):
        # base_url is the 2nd positional parameter of both base clients (after token).
        if len(args) >= 2:
            args = (args[0], _API, *args[2:])
        else:
            kwargs["base_url"] = _API
        original(self, *args, **kwargs)

    __init__._standin_wrapped = True  # type: ignore[attr-defined]
    cls.__init__ = __init__


def _install_sdk() -> None:
    try:
        from slack_sdk.web.async_base_client import AsyncBaseClient
        from slack_sdk.web.base_client import BaseClient
    except ImportError as exc:
        raise SystemExit("Slack loopback stand-in requires slack_sdk") from exc
    _wrap_init(AsyncBaseClient)
    _wrap_init(BaseClient)


def _install_aiohttp() -> None:
    """The adapter's standalone (out-of-gateway) sender POSTs to ``https://slack.com/api/<m>``."""
    try:
        import aiohttp
    except ImportError as exc:
        raise SystemExit("Slack loopback stand-in requires aiohttp") from exc
    original = aiohttp.ClientSession._request
    if getattr(original, "_standin_wrapped", False):
        return

    async def _request(self, method, str_or_url, *args, **kwargs):
        url = str(str_or_url)
        if url.startswith(_REAL_API):
            str_or_url = _API + url[len(_REAL_API):]
        return await original(self, method, str_or_url, *args, **kwargs)

    _request._standin_wrapped = True  # type: ignore[attr-defined]
    aiohttp.ClientSession._request = _request


_API = _API.rstrip("/") + "/"
try:
    _install_sdk()
    _install_aiohttp()
except Exception as exc:
    raise SystemExit(f"Could not install Slack loopback stand-in: {exc}") from exc
