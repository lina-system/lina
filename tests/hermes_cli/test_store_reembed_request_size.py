"""lina#763: `lina store reembed` bounds one embedding request by characters and halves a request the endpoint keeps
answering 429 to, so a batch of long rows finishes instead of stopping the run.

The endpoint answers 429 to any request over a number of characters, as the measured one did; these run on the real
Postgres from tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import json

import httpx
import pytest

from agent.embedding_client import EmbeddingClient, EmbeddingSettings
from hermes_cli.store_reembed import ReembedError, reembed
from tests.hermes_cli.test_store_reembed import _connect, _message
from tests.hermes_cli.test_store_reembed_lcm import _summary

_ROW = 300


class _SizeLimitedEndpoint:
    """Answers 429 to a request of more than *limit* characters, vectors otherwise; records each request's texts."""

    def __init__(self, limit: int) -> None:
        self.limit = limit
        self.requests: list[list[str]] = []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        texts = json.loads(request.content)["input"]
        self.requests.append(texts)
        if sum(len(text) for text in texts) > self.limit:
            return httpx.Response(429, json={"error": "temporarily at capacity upstream"})
        return httpx.Response(200, json={"data": [{"index": i, "embedding": [1.0] * 1024} for i in range(len(texts))]})


def _client(endpoint: _SizeLimitedEndpoint) -> EmbeddingClient:
    settings = EmbeddingSettings(base_url="http://127.0.0.1:9/v1", max_retries=0)
    return EmbeddingClient(settings, transport=httpx.MockTransport(endpoint), sleep=lambda seconds: None)


def _turns(sql, count: int, length: int = _ROW) -> list[int]:
    return [_message(sql, f"agent:default:r{n}", "agent", "user", f"{n:03d}" + "x" * (length - 3))
            for n in range(count)]


def _accepted(endpoint: _SizeLimitedEndpoint) -> list[list[str]]:
    return [texts for texts in endpoint.requests if sum(map(len, texts)) <= endpoint.limit]


def test_a_batch_over_the_request_budget_goes_out_in_requests_within_it(_memory_store, memory_store_sql):
    ids = _turns(memory_store_sql, 6)
    endpoint, lines = _SizeLimitedEndpoint(limit=700), []
    with _client(endpoint) as client:
        counts = reembed(_connect(_memory_store), client, ["agent"], batch_size=6, max_request_chars=700,
                         out=lines.append)
    assert counts[0].embedded == 6 and counts[0].batches == 1
    assert [len(texts) for texts in endpoint.requests] == [2, 2, 2]
    assert f"agent: batch 1, up to id {ids[-1]}, 6 embedded, split into 3 requests" in lines
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings WHERE kind = 'chunk'") == [(6,)]


def test_a_request_the_endpoint_keeps_refusing_with_429_is_halved_until_it_passes(_memory_store, memory_store_sql):
    ids = _turns(memory_store_sql, 8)
    endpoint, lines = _SizeLimitedEndpoint(limit=700), []
    with _client(endpoint) as client:
        counts = reembed(_connect(_memory_store), client, ["agent"], batch_size=8, out=lines.append)
    assert counts[0].embedded == 8 and counts[0].left == 0
    # 8 texts refused, its halves of 4 refused, their halves of 2 pass: every row once, in order.
    assert [len(texts) for texts in endpoint.requests] == [8, 4, 2, 2, 4, 2, 2]
    assert [text for texts in _accepted(endpoint) for text in texts] == [f"{n:03d}" + "x" * (_ROW - 3) for n in range(8)]
    assert f"agent: batch 1, up to id {ids[-1]}, 8 embedded, split into 4 requests" in lines
    assert memory_store_sql("SELECT last_item_id FROM vec.reembed_progress WHERE source = 'agent'") == [(ids[-1],)]


def test_a_single_text_the_endpoint_refuses_stops_the_run_without_a_partial_batch(_memory_store, memory_store_sql):
    ids = _turns(memory_store_sql, 3)
    ids.append(_message(memory_store_sql, "agent:default:long", "agent", "user", "y" * 900))
    endpoint = _SizeLimitedEndpoint(limit=700)
    with _client(endpoint) as client:
        with pytest.raises(ReembedError, match="answered 429 1 times in a row") as stopped:
            reembed(_connect(_memory_store), client, ["agent"], batch_size=2, out=lambda line: None)
    assert "rows left: agent 2" in str(stopped.value)
    assert endpoint.requests[-1] == ["y" * 900]
    # The second batch's first row got its vector, but its batch is not committed without the refused row.
    assert memory_store_sql("SELECT last_item_id FROM vec.reembed_progress WHERE source = 'agent'") == [(ids[1],)]
    assert memory_store_sql("SELECT count(*) FROM vec.embeddings")[0][0] == 2


def test_lcm_summary_batches_are_halved_on_429_too(_memory_store, memory_store_sql):
    for node in range(1, 5):
        _summary(memory_store_sql, "alpha", node, f"{node:03d}" + "s" * (_ROW - 3))
    endpoint, lines = _SizeLimitedEndpoint(limit=700), []
    with _client(endpoint) as client:
        counts = reembed(_connect(_memory_store), client, ["memory"], batch_size=4, out=lines.append)
    assert counts[0].embedded == 4
    assert [len(texts) for texts in endpoint.requests] == [4, 2, 2]
    assert "memory: LCM alpha: up to id 4, 4 embedded, split into 2 requests" in lines
    assert memory_store_sql("SELECT last_item_id FROM vec.reembed_progress WHERE source = 'memory:lcm:alpha'") == [(4,)]


def test_the_request_budget_comes_from_config():
    config = {"embedding": {"base_url": "http://127.0.0.1:9/v1", "max_request_chars": 5000}}
    assert EmbeddingSettings.from_config(config).max_request_chars == 5000
    assert EmbeddingSettings.from_config({"embedding": {"base_url": "http://127.0.0.1:9/v1"}}).max_request_chars == 100_000


def test_the_request_budget_default_is_registered_and_read_through_the_loader(tmp_path, monkeypatch):
    from agent.embedding_client import DEFAULT_MAX_REQUEST_CHARS
    from hermes_cli.config import _validate_config_key, load_config
    from hermes_cli.config_defaults import DEFAULT_CONFIG

    assert DEFAULT_CONFIG["embedding"]["max_request_chars"] == DEFAULT_MAX_REQUEST_CHARS
    assert _validate_config_key("embedding.max_request_chars") == (True, None)
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    assert load_config()["embedding"]["max_request_chars"] == DEFAULT_MAX_REQUEST_CHARS
    (tmp_path / "config.yaml").write_text(
        "embedding:\n  base_url: http://127.0.0.1:9/v1\n  max_request_chars: 5000\n")
    config = load_config()
    assert config["embedding"]["max_request_chars"] == 5000
    assert EmbeddingSettings.from_config(config).max_request_chars == 5000
