"""The board's shared outside reads are served stale while they revalidate (lina#584).

A request waits for an outside read (the nodes' ``hello`` and ``probe``, the running runs' live view, the quota) only
when there is none to serve: before the first one, after a failed one's ``READ_TTL_S``, or past ``STALE_MAX_S``.
Between ``READ_TTL_S`` and ``STALE_MAX_S`` the last good read answers at once with the time it was read, while one
background read replaces it. The slow reads here block on an event, so "at once" means "while the read still runs".
"""

from __future__ import annotations

import logging
import threading
import time

import pytest

from hermes_cli import lina_board, lina_board_devices
from tests.hermes_cli.test_lina_board_api import DEVICES, Fakes
from tests.hermes_cli._work_env import WorkEnv
from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("posix")

KEY = ("contacts", "root")


@pytest.fixture
def clock(monkeypatch):
    now = [1800000000.0]
    monkeypatch.setattr(time, "time", lambda: now[0])
    return now


@pytest.fixture
def reads():
    return lina_board._SharedReads(lina_board.READ_TTL_S, lina_board.STALE_MAX_S)


class Slow:
    """A read that runs until the test lets it end, counting the reads started."""

    def __init__(self, value="new", fail: Exception | None = None):
        self.value, self.fail = value, fail
        self.release = threading.Event()
        self.started = 0

    def __call__(self, _errors):
        self.started += 1
        assert self.release.wait(10)
        if self.fail is not None:
            raise self.fail
        return self.value


def _done(reads, value="old"):
    slow = Slow(value)
    slow.release.set()
    return reads.get(KEY, slow)


def _in_thread(call):
    got: list = []
    thread = threading.Thread(target=lambda: got.append(call()))
    thread.start()
    return thread, got


def test_the_first_read_is_waited_for(reads, clock):
    slow = Slow()
    thread, got = _in_thread(lambda: reads.get(KEY, slow))
    wait_until(lambda: slow.started == 1, timeout=10)

    assert got == [] and thread.is_alive()
    slow.release.set()
    thread.join(10)
    assert got[0].result() == "new" and got[0].at == clock[0]


def test_a_read_past_its_ttl_answers_at_once_while_one_new_read_runs_behind_it(reads, clock):
    old = _done(reads)
    clock[0] += lina_board.READ_TTL_S
    slow = Slow()

    started = time.monotonic()
    first, second = reads.get(KEY, slow), reads.get(KEY, slow)

    assert time.monotonic() - started < 2
    assert first is old and second is old and old.result() == "old"
    assert reads.running("contacts") == 1
    wait_until(lambda: slow.started == 1, timeout=10)
    slow.release.set()
    wait_until(lambda: reads.running("contacts") == 0, timeout=10)
    new = reads.get(KEY, Slow("unused"))
    assert new.result() == "new" and new.at == clock[0] and old.at == clock[0] - lina_board.READ_TTL_S


def test_a_read_past_stale_max_is_waited_for(reads, clock):
    _done(reads)
    clock[0] += lina_board.STALE_MAX_S
    slow = Slow()
    thread, got = _in_thread(lambda: reads.get(KEY, slow))
    wait_until(lambda: slow.started == 1, timeout=10)

    assert got == [] and thread.is_alive()
    slow.release.set()
    thread.join(10)
    assert got[0].result() == "new"


def test_a_failed_read_answers_for_its_ttl_and_is_never_served_stale(reads, clock):
    failing = Slow(fail=RuntimeError("ssh: build-a: timed out"))
    failing.release.set()
    with pytest.raises(RuntimeError, match="timed out"):
        reads.get(KEY, failing).result()
    clock[0] += lina_board.READ_TTL_S - 1
    with pytest.raises(RuntimeError, match="timed out"):
        reads.get(KEY, Slow("unused")).result()

    clock[0] += 1
    slow = Slow()
    thread, got = _in_thread(lambda: reads.get(KEY, slow))
    wait_until(lambda: slow.started == 1, timeout=10)
    assert got == [] and thread.is_alive()
    slow.release.set()
    thread.join(10)
    assert got[0].result() == "new"


def test_a_failed_background_read_replaces_the_stale_answer(reads, clock):
    _done(reads)
    clock[0] += lina_board.READ_TTL_S
    failing = Slow(fail=RuntimeError("ssh: build-a: timed out"))
    assert reads.get(KEY, failing).result() == "old"
    failing.release.set()
    wait_until(lambda: reads.running("contacts") == 0, timeout=10)

    with pytest.raises(RuntimeError, match="timed out"):
        reads.get(KEY, Slow("unused")).result()


def test_devices_past_the_ttl_answer_at_once_with_the_time_they_were_read(tmp_path, monkeypatch):
    work = WorkEnv(tmp_path, monkeypatch)
    work.config(extra=DEVICES)
    Fakes(work, monkeypatch)
    contact = lina_board_devices._contact
    gate = threading.Event()
    gate.set()

    def gated(*args):
        assert gate.wait(10)
        return contact(*args)
    monkeypatch.setattr(lina_board_devices, "_contact", gated)
    read_at = work.clock[0]
    first = lina_board.devices()
    work.clock[0] += lina_board.READ_TTL_S
    gate.clear()

    started = time.monotonic()
    second = lina_board.devices()

    assert time.monotonic() - started < 2
    assert second["at"] == read_at and second["machines"] == first["machines"]
    assert lina_board.READS.running("contacts") == 1
    gate.set()
    wait_until(lambda: lina_board.READS.running("contacts") == 0, timeout=10)
    assert lina_board.devices()["at"] == read_at + lina_board.READ_TTL_S


def test_the_read_at_server_start_runs_behind_the_start_and_a_failure_only_warns(monkeypatch, caplog):
    gate = threading.Event()

    def board():
        assert gate.wait(10)
        raise RuntimeError("no record")
    monkeypatch.setattr(lina_board, "board", board)

    with caplog.at_level(logging.WARNING, logger="hermes_cli.lina_board"):
        thread = lina_board.warm()
        assert thread.is_alive()
        gate.set()
        thread.join(10)

    assert not thread.is_alive()
    assert "board: the read at server start failed" in caplog.text
