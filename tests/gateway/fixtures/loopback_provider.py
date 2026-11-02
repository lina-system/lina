"""Scripted loopback provider for owner recovery requests."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

_MODEL = "fixture-model"

class _LoopbackProvider:
    """Scripted OpenAI-compatible endpoint: one ``{"finish_reason", "content"}`` per request."""

    def __init__(self) -> None:
        self.script: list[dict] = []
        self.requests: list[dict] = []
        provider = self

        class _Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                provider.requests.append(body)
                spec = provider.script.pop(0)
                delta = {"role": "assistant", "content": spec["content"]}
                usage = {"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14}
                if body.get("stream"):
                    chunks = [
                        {"id": "c", "object": "chat.completion.chunk", "created": 1, "model": _MODEL,
                         "choices": [{"index": 0, "delta": delta, "finish_reason": None}]},
                        {"id": "c", "object": "chat.completion.chunk", "created": 1, "model": _MODEL,
                         "choices": [{"index": 0, "delta": {}, "finish_reason": spec["finish_reason"]}],
                         "usage": usage},
                    ]
                    mime, payload = "text/event-stream", (
                        "".join("data: " + json.dumps(c) + "\n\n" for c in chunks) + "data: [DONE]\n\n"
                    ).encode()
                else:
                    mime, payload = "application/json", json.dumps({
                        "id": "c", "object": "chat.completion", "created": 1, "model": _MODEL,
                        "choices": [{"index": 0, "message": delta, "finish_reason": spec["finish_reason"]}],
                        "usage": usage,
                    }).encode()
                self.send_response(200)
                self.send_header("Content-Type", mime)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
        threading.Thread(target=self._server.serve_forever, daemon=True).start()
        self.base_url = f"http://127.0.0.1:{self._server.server_port}/v1"

    def shutdown(self) -> None:
        self._server.shutdown()
