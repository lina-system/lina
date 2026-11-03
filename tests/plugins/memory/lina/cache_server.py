"""A loopback OpenAI-compatible model that reports usage the way Anthropic's prompt cache does (lina#25).

A request's cached prefix is the longest byte prefix it shares with any request of the last hour (a read refreshes
that request's hour), counted in whole 1024-token blocks at 4 bytes a token. Every whole block past the read is
written. Each request also becomes one OCX usage row (``inputTokens`` with reads and writes included), so the rows
go to ``lina memory cache-report`` as they would from OCX. The model writes everything to the 1h tier: its cache
lives an hour.
"""

from __future__ import annotations

import json
import threading
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Callable

BYTES_PER_TOKEN = 4
BLOCK_TOKENS = 1024
TTL = timedelta(hours=1)


def wire(body: dict) -> bytes:
    """The request in the order the provider caches it: tools, then each message."""
    parts = [json.dumps(body.get("tools"), sort_keys=True)]
    parts += [json.dumps(m, sort_keys=True, ensure_ascii=False) for m in body["messages"]]
    return "".join(parts).encode()


def _common(a: bytes, b: bytes) -> int:
    a, b = memoryview(a), memoryview(b)
    lo, hi = 0, min(len(a), len(b))
    while lo < hi:
        mid = (lo + hi + 1) // 2
        if a[:mid] == b[:mid]:
            lo = mid
        else:
            hi = mid - 1
    return lo


@dataclass
class PromptCache:
    entries: list[list] = field(default_factory=list)

    def account(self, data: bytes, now: datetime) -> tuple[int, int, int]:
        """(input total, cache read, cache write) in tokens for one request at ``now``."""
        self.entries = [e for e in self.entries if now - e[1] <= TTL]
        best, common = None, 0
        for entry in self.entries:
            if len(entry[0]) <= common:
                continue
            length = _common(entry[0], data)
            if length > common:
                best, common = entry, length
        if best is not None:
            best[1] = now
        total = -(-len(data) // BYTES_PER_TOKEN)
        read = common // BYTES_PER_TOKEN // BLOCK_TOKENS * BLOCK_TOKENS
        write = max(0, total // BLOCK_TOKENS * BLOCK_TOKENS - read)
        self.entries.append([data, now])
        return total, read, write


class CacheModel(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        server = self.server
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        now = server.clock()
        total, read, write = server.cache.account(wire(body), now)
        reply = f"reply {len(server.usage) + 1}: noted."
        output = -(-len(reply) // BYTES_PER_TOKEN)
        server.usage.append({
            "timestamp": now.isoformat(), "conversationId": server.conversation, "requestId": f"req-{len(server.usage)}",
            "inputTokens": total, "cacheReadInputTokens": read, "cacheCreationInputTokens": write,
            "cacheCreation1hInputTokens": write, "outputTokens": output})
        server.compactions.append(server.compaction_count())
        message = {"role": "assistant", "content": reply}
        usage = {"prompt_tokens": total, "completion_tokens": output, "total_tokens": total + output,
                 "prompt_tokens_details": {"cached_tokens": read}}
        if body.get("stream"):
            chunks = [{"id": "r", "object": "chat.completion.chunk", "model": "m",
                       "choices": [{"index": 0, "delta": message, "finish_reason": "stop"}]},
                      {"id": "r", "object": "chat.completion.chunk", "model": "m", "choices": [], "usage": usage}]
            payload = "".join(f"data: {json.dumps(c)}\n\n" for c in chunks).encode() + b"data: [DONE]\n\n"
            kind = "text/event-stream"
        else:
            payload = json.dumps({"id": "r", "object": "chat.completion", "model": "m", "usage": usage,
                                  "choices": [{"index": 0, "message": message, "finish_reason": "stop"}]}).encode()
            kind = "application/json"
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


@contextmanager
def cache_model(clock: Callable[[], datetime], conversation: str):
    server = ThreadingHTTPServer(("127.0.0.1", 0), CacheModel)
    server.clock, server.conversation, server.cache = clock, conversation, PromptCache()
    server.usage, server.compactions = [], []
    server.compaction_count = lambda: 0
    server.url = f"http://127.0.0.1:{server.server_port}/v1"
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
