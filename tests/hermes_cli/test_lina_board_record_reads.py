"""The web server's record reads run side by side (lina#729): the work package's binding and store connection are
the reading context's own, so a read that is slow on the store (a psql round trip) holds up no other read."""

from __future__ import annotations

import threading

import pytest

from hermes_cli import lina_deps
from tests.hermes_cli._work_env import WorkEnv

pytestmark = pytest.mark.platforms("posix")

HELD = "held-record-read"


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_deps, "lina_root", lambda: env.root)
    env.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "빌드 기기 정리", "--basis", "기기 정리해 줘",
           "--done", "빌드 기기가 모두 가동된다", "--check", "false")
    env.ok("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", "기기 1 점검", "--why", "멈추지 않게",
           "--done", "기기가 답한다", "--check", "false", "--omo", "next")
    return env


def test_a_record_read_does_not_wait_for_one_still_on_the_store(work, monkeypatch):
    from hermes_cli import lina_board, lina_board_items
    from hermes_cli.work import ledger
    real, entered, release = ledger.read_all, threading.Event(), threading.Event()

    def read_all():
        if threading.current_thread().name == HELD:
            entered.set()
            assert release.wait(30)  # longer than the others are given, so a read that waits shows as waiting
        return real()
    monkeypatch.setattr(ledger, "read_all", read_all)
    results: dict[str, object] = {}

    def run(name, read):
        results[name] = read()
    held = threading.Thread(target=run, args=("held", lina_board_items.items), name=HELD)
    held.start()
    assert entered.wait(10)
    others = [threading.Thread(target=run, args=args) for args in (
        ("items", lina_board_items.items), ("board", lambda: lina_board.work_read(lina_board._root())))]
    for thread in others:
        thread.start()
    for thread in others:
        thread.join(10)
    waiting = [thread for thread in others if thread.is_alive()]
    release.set()
    held.join(30)

    assert not waiting and not held.is_alive()
    assert results["held"] == results["items"] and [r["title"] for r in results["items"]["items"]]
    assert results["board"]["agents"]
