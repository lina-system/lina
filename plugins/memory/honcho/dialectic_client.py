"""The Honcho client dialectic queries run on: no SDK retries, and abortable at shutdown.

The server does not cancel a non-streaming dialectic when its client disconnects, and one run takes
40-60 s of tool calls. An SDK retry after a timeout therefore starts the same dialectic again (#714),
so ``peer.chat`` gets its own client built with ``max_retries=0``; reads and writes keep the shared
client and its retries. The client is per session manager so one manager's shutdown can abort its own
in-flight query (#712) without touching another manager's. Closing an httpx pool does not wake a
thread blocked in a read, so the client records its sockets through httpcore's ``trace`` request
extension and shuts them down instead.
"""

from __future__ import annotations

import contextlib
import socket
import threading
import weakref
from typing import TYPE_CHECKING, Any

import httpx

if TYPE_CHECKING:
    from honcho import Honcho

    from .client import HonchoClientConfig

# httpcore trace events whose return value is the connection's network stream.
_STREAM_EVENTS = frozenset({"connection.connect_tcp.complete", "connection.start_tls.complete"})


class DialecticClient:
    """A no-retry SDK client for ``peer.chat`` bound to ``source``, the shared client it follows."""

    def __init__(self, config: HonchoClientConfig | None, source: Honcho):
        from .client import HonchoClientConfig, _build_client

        self.source = source
        self.peers: dict[str, Any] = {}
        self._lock = threading.Lock()
        self._sockets: weakref.WeakSet[socket.socket] = weakref.WeakSet()
        self._closed = False
        self._http = httpx.Client(event_hooks={"request": [self._trace_request]})
        self.honcho = _build_client(config or HonchoClientConfig.from_global_config(), max_retries=0,
                                    http_client=self._http)
        self.sync_token()

    @property
    def closed(self) -> bool:
        return self._closed

    def sync_token(self) -> None:
        """Carry the shared client's bearer over: OAuth refreshes rotate only the shared client in place."""
        from .oauth import apply_token_to_client

        token = getattr(getattr(self.source, "_http", None), "api_key", None)
        if token:
            apply_token_to_client(self.honcho, token)

    def _trace_request(self, request: httpx.Request) -> None:
        request.extensions["trace"] = self._trace

    def _trace(self, event: str, info: dict[str, Any]) -> None:
        if event not in _STREAM_EVENTS:
            return
        sock = info["return_value"].get_extra_info("socket")
        with self._lock:
            if not self._closed:
                self._sockets.add(sock)
                return
        # Connected after close() swept the sockets: end this request too.
        _shutdown(sock)

    def close(self) -> None:
        """Abort every request in flight on this client and refuse new ones."""
        with self._lock:
            self._closed = True
            sockets = list(self._sockets)
        for sock in sockets:
            _shutdown(sock)
        self._http.close()


def _shutdown(sock: socket.socket) -> None:
    # OSError: the socket is already closed, or was detached when TLS wrapped it.
    with contextlib.suppress(OSError):
        sock.shutdown(socket.SHUT_RDWR)
