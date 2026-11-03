"""lina#19: the one embedding client and the Matryoshka truncation of stored vectors."""

from __future__ import annotations

import json
import math
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from agent.embedding_client import (
    EmbeddingClient,
    EmbeddingError,
    EmbeddingModelMismatch,
    EmbeddingSettings,
    matryoshka,
    truncate_stored,
)


def _full_width(seed: int, width: int = 4096) -> list[float]:
    return [((seed * 31 + i * 7) % 97 - 48) / 48.0 for i in range(width)]


class _FakeEmbeddings:
    """An OpenAI-compatible /v1/embeddings that ignores ``dimensions`` and answers at full width, out of order."""

    def __init__(self, status: int = 200) -> None:
        self.requests: list[dict] = []
        self.status = status
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                fake.requests.append({"path": self.path, "auth": self.headers.get("Authorization"), "body": body})
                if fake.status != 200:
                    payload = b'{"error": "overloaded"}'
                else:
                    data = [{"index": i, "embedding": _full_width(len(text))} for i, text in enumerate(body["input"])]
                    payload = json.dumps({"data": list(reversed(data))}).encode()
                self.send_response(fake.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *args) -> None:
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_address[1]}/v1"

    def __enter__(self) -> _FakeEmbeddings:
        self.thread.start()
        return self

    def __exit__(self, *exc: object) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=10)


def _norm(vector: list[float]) -> float:
    return math.sqrt(sum(v * v for v in vector))


def test_client_cuts_full_width_answers_to_unit_prefixes_in_input_order_and_batches():
    with _FakeEmbeddings() as fake:
        settings = EmbeddingSettings(base_url=fake.base_url, model="qwen3-embedding-8b", dimensions=1024,
                                     api_key="test-key", batch_size=2)
        with EmbeddingClient(settings) as client:
            texts = ["a", "bb", "ccc", "dddd", "eeeee"]
            vectors = client.embed(texts)

    assert [len(r["body"]["input"]) for r in fake.requests] == [2, 2, 1]
    first = fake.requests[0]
    assert first["path"] == "/v1/embeddings"
    assert first["auth"] == "Bearer test-key"
    assert first["body"]["model"] == "qwen3-embedding-8b"
    assert first["body"]["dimensions"] == 1024
    assert len(vectors) == 5
    for text, vector in zip(texts, vectors):
        assert len(vector) == 1024
        assert _norm(vector) == pytest.approx(1.0)
        assert vector == pytest.approx(matryoshka(_full_width(len(text)), 1024))


def test_client_reports_an_error_status_as_embedding_error():
    with _FakeEmbeddings(status=503) as fake:
        with EmbeddingClient(EmbeddingSettings(base_url=fake.base_url)) as client:
            with pytest.raises(EmbeddingError, match="503"):
                client.embed(["hello"])


def test_settings_come_from_config_and_the_key_from_the_named_env_var(monkeypatch):
    monkeypatch.setenv("EVAL_EMBED_KEY", "from-env")
    settings = EmbeddingSettings.from_config({"embedding": {
        "base_url": "http://127.0.0.1:9/v1/", "model": "qwen3-embedding-8b", "dimensions": 1024,
        "api_key_env": "EVAL_EMBED_KEY", "batch_size": 8}})
    assert settings.base_url == "http://127.0.0.1:9/v1"
    assert settings.api_key == "from-env"
    assert (settings.model, settings.dimensions, settings.batch_size) == ("qwen3-embedding-8b", 1024, 8)


def test_settings_without_an_endpoint_are_refused():
    with pytest.raises(EmbeddingError, match="embedding.base_url"):
        EmbeddingSettings.from_config({"embedding": {"model": "qwen3-embedding-8b"}})


@pytest.mark.parametrize("width", [4096, 1536])
def test_a_stored_full_width_vector_becomes_its_renormalised_1024_prefix(width):
    stored = _full_width(3, width)
    cut = truncate_stored(stored, stored_model="qwen3-embedding-8b", model="qwen3-embedding-8b")
    norm = _norm(stored[:1024])
    assert len(cut) == 1024
    assert _norm(cut) == pytest.approx(1.0)
    assert cut == pytest.approx([v / norm for v in stored[:1024]])


def test_a_vector_from_another_model_is_refused():
    with pytest.raises(EmbeddingModelMismatch, match="text-embedding-3-small"):
        truncate_stored(_full_width(1, 1536), stored_model="text-embedding-3-small", model="qwen3-embedding-8b")


def test_a_vector_narrower_than_the_target_or_all_zero_is_refused():
    with pytest.raises(ValueError, match="fewer than"):
        truncate_stored([1.0] * 512, stored_model="m", model="m")
    with pytest.raises(ValueError, match="zero"):
        truncate_stored([0.0] * 2048, stored_model="m", model="m")
