"""lina#652, lina#740: the embedding client waits and sends a batch again on 429, 5xx and a timeout, and keeps under a
request rate."""

from __future__ import annotations

import json

import httpx
import pytest

from agent.embedding_client import EmbeddingClient, EmbeddingRetriesExhausted, EmbeddingSettings


class _Endpoint:
    """Answers each POST with the next of *answers* (status, headers), the last one for every request after."""

    def __init__(self, *answers: tuple[int, dict[str, str]], width: int = 8) -> None:
        self.answers = list(answers)
        self.width = width
        self.inputs: list[list[str]] = []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        texts = json.loads(request.content)["input"]
        self.inputs.append(texts)
        status, headers = self.answers.pop(0) if len(self.answers) > 1 else self.answers[0]
        if status != 200:
            return httpx.Response(status, headers=headers, json={"error": "rate limited"})
        return httpx.Response(200, json={"data": [{"index": i, "embedding": [1.0] * self.width} for i in range(len(texts))]})


def _client(endpoint: _Endpoint, sleeps: list[float], **settings) -> EmbeddingClient:
    return EmbeddingClient(EmbeddingSettings(base_url="http://127.0.0.1:9/v1", dimensions=8, **settings),
                           transport=httpx.MockTransport(endpoint), sleep=sleeps.append)


def test_a_429_then_success_sends_the_same_batch_again_and_continues():
    endpoint, sleeps = _Endpoint((429, {}), (200, {})), []
    with _client(endpoint, sleeps) as client:
        vectors = client.embed(["a", "b"])
    assert len(vectors) == 2
    assert endpoint.inputs == [["a", "b"], ["a", "b"]]
    assert sleeps == [1.0]


def test_retry_after_is_honoured():
    endpoint, sleeps = _Endpoint((429, {"Retry-After": "7"}), (503, {}), (200, {})), []
    with _client(endpoint, sleeps) as client:
        client.embed(["a"])
    assert sleeps == [7.0, 2.0]


def test_a_persistent_429_stops_after_max_retries_with_the_pauses_doubling_up_to_the_cap():
    endpoint, sleeps = _Endpoint((429, {})), []
    with _client(endpoint, sleeps) as client:
        with pytest.raises(EmbeddingRetriesExhausted, match="answered 429 9 times in a row"):
            client.embed(["a"])
    assert len(endpoint.inputs) == 9
    assert sleeps == [1.0, 2.0, 4.0, 8.0, 16.0, 32.0, 60.0, 60.0]


def test_a_success_starts_the_count_of_failures_over():
    endpoint, sleeps = _Endpoint((429, {}), (200, {}), (429, {}), (200, {})), []
    with _client(endpoint, sleeps, max_retries=1, batch_size=1) as client:
        assert len(client.embed(["a", "b"])) == 2
    assert sleeps == [1.0, 1.0]


def test_max_rate_spaces_the_requests():
    now, sleeps = [100.0], []

    def sleep(seconds: float) -> None:
        sleeps.append(seconds)
        now[0] += seconds

    settings = EmbeddingSettings(base_url="http://127.0.0.1:9/v1", dimensions=8, batch_size=1)
    with EmbeddingClient(settings, transport=httpx.MockTransport(_Endpoint((200, {}))), max_rate=4,
                         sleep=sleep, clock=lambda: now[0]) as client:
        client.embed(["a", "b", "c"])
    assert sleeps == [0.25, 0.25]


def test_max_retries_comes_from_config():
    config = {"embedding": {"base_url": "http://127.0.0.1:9/v1", "max_retries": 3}}
    assert EmbeddingSettings.from_config(config).max_retries == 3
    assert EmbeddingSettings.from_config({"embedding": {"base_url": "http://127.0.0.1:9/v1"}}).max_retries == 8


class _TimingOut:
    """Raises a read timeout for the first *timeouts* requests, then answers 200."""

    def __init__(self, timeouts: int, width: int = 8) -> None:
        self.timeouts = timeouts
        self.width = width
        self.calls = 0

    def __call__(self, request: httpx.Request) -> httpx.Response:
        self.calls += 1
        if self.calls <= self.timeouts:
            raise httpx.ReadTimeout("timed out", request=request)
        texts = json.loads(request.content)["input"]
        return httpx.Response(200, json={"data": [{"index": i, "embedding": [1.0] * self.width} for i in range(len(texts))]})


def _timing_client(endpoint: _TimingOut, sleeps: list[float], **settings) -> EmbeddingClient:
    return EmbeddingClient(EmbeddingSettings(base_url="http://127.0.0.1:9/v1", dimensions=8, **settings),
                           transport=httpx.MockTransport(endpoint), sleep=sleeps.append)


def test_a_timeout_then_success_sends_the_batch_again_after_the_same_pause_as_a_429():
    endpoint, sleeps = _TimingOut(2), []
    with _timing_client(endpoint, sleeps) as client:
        assert len(client.embed(["a", "b"])) == 2
    assert endpoint.calls == 3
    assert sleeps == [1.0, 2.0]


def test_a_persistent_timeout_stops_after_max_retries_and_names_the_timeout():
    endpoint, sleeps = _TimingOut(10**6), []
    with _timing_client(endpoint, sleeps, max_retries=2, timeout=7.0) as client:
        with pytest.raises(EmbeddingRetriesExhausted, match=r"timed out 3 times in a row \(embedding.timeout 7 s"):
            client.embed(["a"])
    assert endpoint.calls == 3
    assert sleeps == [1.0, 2.0]


def test_timeouts_and_429s_share_one_count_of_failures_in_a_row():
    answers = iter(["timeout", 429, "timeout"])

    def handler(request: httpx.Request) -> httpx.Response:
        answer = next(answers)
        if answer == "timeout":
            raise httpx.ReadTimeout("timed out", request=request)
        return httpx.Response(429, json={"error": "rate limited"})

    sleeps: list[float] = []
    client = EmbeddingClient(EmbeddingSettings(base_url="http://127.0.0.1:9/v1", dimensions=8, max_retries=2),
                             transport=httpx.MockTransport(handler), sleep=sleeps.append)
    with client:
        with pytest.raises(EmbeddingRetriesExhausted):
            client.embed(["a"])
    assert sleeps == [1.0, 2.0]
