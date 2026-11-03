"""lina#25: the fake prompt cache's accounting, on its own (a known prefix gives the known read count)."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

from tests.plugins.memory.lina.cache_server import BYTES_PER_TOKEN as TOKENS
from tests.plugins.memory.lina.cache_server import PromptCache

T0 = datetime(2026, 11, 3, 9, 0, tzinfo=UTC)


def _bytes(tokens: int, fill: bytes = b"a") -> bytes:
    return fill * (tokens * TOKENS)


def test_first_request_writes_its_whole_blocks_and_reads_nothing():
    assert PromptCache().account(_bytes(5_000), T0) == (5_000, 0, 4_096)


def test_extension_reads_the_shared_whole_blocks_and_writes_the_rest():
    cache = PromptCache()
    first = _bytes(5_000)
    cache.account(first, T0)
    assert cache.account(first + _bytes(2_000, b"b"), T0 + timedelta(minutes=1)) == (7_000, 4_096, 2_048)


def test_prefix_is_the_longest_match_with_any_earlier_request():
    cache = PromptCache()
    a, b = _bytes(3_000, b"a"), _bytes(3_000, b"b")
    cache.account(a, T0)
    cache.account(b, T0)
    assert cache.account(a[:2_100 * TOKENS] + _bytes(900, b"c"), T0) == (3_000, 2_048, 0)


def test_a_block_unused_for_an_hour_expires_and_a_read_refreshes_the_blocks_it_read():
    data = _bytes(4_096)
    half = data[:2_048 * TOKENS] + _bytes(2_048, b"z")
    refreshed = PromptCache()
    refreshed.account(data, T0)
    assert refreshed.account(half, T0 + timedelta(minutes=50))[1] == 2_048
    assert refreshed.account(data, T0 + timedelta(minutes=109))[1] == 2_048
    lone = PromptCache()
    lone.account(data, T0)
    assert lone.account(data, T0 + timedelta(minutes=61))[1] == 0
