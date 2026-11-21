"""Point discord.py at a local Discord stand-in (tests only).

discord.py has no base-URL knob: REST URLs are built from the class attribute
``discord.http.Route.BASE`` and the gateway socket from
``discord.gateway.DiscordWebSocket.DEFAULT_GATEWAY`` (non-sharded ``Client.connect`` never calls
``GET /gateway/bot``; the resume URL comes from READY's ``resume_gateway_url``, which the stand-in
also points at itself). This module is put on a child process's ``PYTHONPATH`` by the Discord driver
and requires both local stand-in endpoints. It patches the SDK only; no Hermes
code is touched. Side effect: when set, discord.py (and yarl/aiohttp) are imported eagerly at
interpreter startup, before Hermes runs, rather than lazily by the adapter.
"""

import os
import re

_api = os.environ.get("HERMES_STANDIN_DISCORD_API", "")
_gw = os.environ.get("HERMES_STANDIN_DISCORD_GATEWAY", "")
if not (re.fullmatch(r"http://127\.0\.0\.1:[1-9]\d*/api/v10", _api)
        and re.fullmatch(r"ws://127\.0\.0\.1:[1-9]\d*/gw", _gw)):
    # A regular exception in sitecustomize would let the application start.
    raise SystemExit("Discord tests require API and gateway loopback stand-ins")

try:
    import yarl
    import discord.gateway
    import discord.http

    discord.http.Route.BASE = _api
    discord.gateway.DiscordWebSocket.DEFAULT_GATEWAY = yarl.URL(_gw)
    os.environ["HERMES_STANDIN_DISCORD_SHIM_LOADED"] = "1"
except Exception as exc:
    raise SystemExit(f"Could not install Discord loopback stand-in: {exc}") from exc
