"""Gzip for the web server's responses (lina#729): JSON, pages and bundle assets went over the wire raw.

Starlette's ``GZipMiddleware`` already leaves alone what must not be re-encoded: WebSocket traffic, ``text/event-stream``,
media and archive types, partial (206) responses and bodies that carry a ``Content-Encoding``; bodies of 128 KiB and
more are compressed in a worker thread, so a large card list does not hold the event loop.
"""

from __future__ import annotations

from fastapi import FastAPI
from starlette.middleware.gzip import GZipMiddleware

# Below this the gzip framing and the work cost more than they save.
MINIMUM_SIZE = 1024
# zlib's default: within a few percent of level 9's size at a fraction of its time on the large JSON replies.
COMPRESS_LEVEL = 6


def add_compression(app: FastAPI) -> None:
    app.add_middleware(GZipMiddleware, minimum_size=MINIMUM_SIZE, compresslevel=COMPRESS_LEVEL)
