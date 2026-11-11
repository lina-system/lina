"""``GET /api/lina/board`` reads its sources at once and shares one outside read for a few seconds (lina#573).

The four reads (the work record, ``lina worker``'s runs, the devices, the quota) mostly wait on ssh and OCX, so the
snapshot takes as long as the slowest of them, not their sum; a request that comes while an outside read runs waits for
that read instead of starting its own, and its answer serves every request for ``READ_TTL_S`` seconds (then, stale, while
one background read replaces it: lina#584, ``test_lina_board_stale.py``). The read marks
are not part of that answer: a mark written now shows in the next snapshot.
"""

from __future__ import annotations

import threading

import pytest

from hermes_cli import lina_board, lina_board_write
from tests.hermes_cli.test_lina_board_api import DEVICES, Fakes, _jobs, _record, _valid
from tests.hermes_cli._work_env import WorkEnv
from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("posix")


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.config(extra=DEVICES)
    return env


@pytest.fixture
def fakes(work, monkeypatch):
    return Fakes(work, monkeypatch)


class Calls:
    def __init__(self, monkeypatch, reads: dict):
        self.count = dict.fromkeys(reads, 0)
        self.lock = threading.Lock()

        def counted(name, read):
            def run(*args):
                with self.lock:
                    self.count[name] += 1
                return read(*args)
            return run
        monkeypatch.setattr(lina_board, "work_read", counted("work", reads["work"]))
        monkeypatch.setattr(lina_board, "worker_read", counted("worker", reads["worker"]))
        monkeypatch.setattr(lina_board, "devices_read", counted("devices", reads["devices"]))
        monkeypatch.setattr(lina_board, "quota_read", counted("quota", reads["quota"]))


def test_the_four_reads_run_at_once_not_one_after_another(work, monkeypatch):
    """Each read waits until all four have started: read one after another, the first never sees the others and
    gives up, so the snapshot holds every value only when they run at the same time."""
    started = threading.Barrier(4, timeout=4)

    def meet(value):
        def read(*_args):
            started.wait()
            return value
        return read
    Calls(monkeypatch, {"work": meet(None), "worker": meet([]), "devices": meet({"machines": []}),
                        "quota": meet([])})

    board = lina_board.board()

    assert board["errors"] == []
    assert board["counts"]["running"] == 0 and board["machines"] == [] and board["quota"] == []


def test_a_failing_source_leaves_the_others_and_is_named_in_errors(work, fakes, monkeypatch):
    _record(work)
    _jobs(fakes)

    def broken(_root, _now):
        raise RuntimeError("ssh: build-a: connection refused")
    monkeypatch.setattr(lina_board, "worker_read", broken)

    board = _valid("BoardSnapshot", lina_board.board())

    assert [(e["source"], e["message"]) for e in board["errors"]] == [
        ("worker", "RuntimeError: ssh: build-a: connection refused")]
    assert [row["id"] for row in board["inbox"]["mine"]] == ["T3", "T4"]
    assert [m["name"] for m in board["machines"]] == ["build-a", "here"]
    assert board["cards"] == [] and board["counts"]["running"] is None


def test_overlapping_requests_share_one_outside_read_until_it_expires(work, fakes, monkeypatch):
    """Counted where the board leaves the host: a running run's live view (ssh to its device), the nodes' contacts
    (ssh to each node) and the quota read (OCX)."""
    from hermes_cli import lina_board_devices
    from hermes_cli.worker import hub
    fakes.job("OT-ALP0001", heading="slow device")
    release = threading.Event()
    count = {"meta": 0, "contact": 0, "quota": 0}
    lock = threading.Lock()

    def counted(name, call, wait=False):
        def run(*args, **kwargs):
            with lock:
                count[name] += 1
            if wait:
                assert release.wait(10)
            return call(*args, **kwargs)
        return run
    monkeypatch.setattr(hub.Hub, "meta", counted("meta", hub.Hub.meta, wait=True))
    monkeypatch.setattr(lina_board_devices, "for_device", counted("contact", lina_board_devices.for_device))
    monkeypatch.setattr(lina_board, "quota_read", counted("quota", lina_board.quota_read))

    answers: list[dict] = []
    first = threading.Thread(target=lambda: answers.append(lina_board.board()))
    first.start()
    wait_until(lambda: count["meta"] == 1, timeout=10)
    second = threading.Thread(target=lambda: answers.append(lina_board.board()))
    second.start()
    wait_until(lambda: lina_board.READS.waiting("lives") == 1, timeout=10)
    release.set()
    first.join(10)
    second.join(10)

    assert [[card["id"] for card in a["cards"]] for a in answers] == [["OT-ALP0001"], ["OT-ALP0001"]]
    assert count == {"meta": 1, "contact": 2, "quota": 1}
    work.clock[0] += lina_board.READ_TTL_S - 1
    lina_board.board()
    assert count == {"meta": 1, "contact": 2, "quota": 1}
    work.clock[0] += 1
    lina_board.board()
    wait_until(lambda: count == {"meta": 2, "contact": 4, "quota": 2}, timeout=10)


def test_a_shared_read_keeps_the_time_it_was_read(work, fakes):
    read_at = work.clock[0]
    assert lina_board.devices()["at"] == read_at
    work.clock[0] += 1
    assert lina_board.devices()["at"] == read_at
    assert lina_board.board()["at"] == read_at


def test_a_read_mark_shows_in_the_next_snapshot_while_the_reads_are_shared(work, fakes):
    _record(work)
    _jobs(fakes)
    before = lina_board.board()
    key = before["notices"][0]["key"]
    assert before["notices"][0]["read"] is False

    lina_board_write.mark_read({"keys": [key]})
    after = lina_board.board()

    assert next(note for note in after["notices"] if note["key"] == key)["read"] is True
    assert after["counts"]["unread"] == before["counts"]["unread"] - 1
