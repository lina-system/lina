"""The board's worker views and its run-event stream read the root ``lina worker`` writes (lina#269).

``worker/settings.py`` ``lina_home()`` (lina#227) is the worker's root: ``LINA_HOME`` when set, and a
``HERMES_HOME`` of Hermes Agent's (a Hermes card session's) ignored for the platform default. The board resolved its
own root instead (``lina_ocx.lina_root``, the ``HERMES_HOME`` default) and handed that to ``worker_read``,
``devices_read`` and the event stream, so inside a card session, or whenever the two roots differed, it read worker
state nobody wrote: no runs, no devices, no run events.

``lina_ocx.lina_root`` is deliberately NOT patched here — a patched root is exactly what made the two agree and hid
this case. The run is written through the hub's own store under ``settings.root()``, which resolves ``lina_home()``
itself, the same way ``lina worker submit`` does.
"""

from __future__ import annotations

import pytest

import hermes_constants
from hermes_cli import lina_board, lina_board_events, lina_ocx
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")

DEVICES = "devices:\n  workers:\n    here: {host: local, cap: 1}\n"
READY = {"runner": "omo", "ready": True, "serve": True, "running": 1, "cores": 8, "load": 0.5, "free_gb": 100,
         "repo": True}


@pytest.fixture
def split_roots(tmp_path, monkeypatch):
    """``LINA_HOME`` at the worker's root and ``HERMES_HOME`` at a Hermes Agent home: the worker writes the first and
    the board's own root (``lina_ocx.lina_root``, left unpatched) is the second. Both are under *tmp_path*."""
    env = WorkEnv(tmp_path, monkeypatch)
    env.config(extra=DEVICES)  # the LINA root's config: the devices and the worker's settings
    hermes_agent = tmp_path / "account" / ".hermes"
    hermes_agent.mkdir(parents=True)
    (hermes_agent / "config.yaml").write_text(
        "work:\n  locale: en\n  owner: {id: owner, name: owner}\n  agents: {alpha: Alpha}\n  store: {backend: sqlite}\n",
        encoding="utf-8")
    # A Hermes Agent home this test owns: the account's real one is ~/.hermes, which no test may write to.
    monkeypatch.setattr(hermes_constants, "_account_home", lambda: tmp_path / "account")
    monkeypatch.setattr(hermes_constants, "_default_hermes_root_memo", None)
    monkeypatch.setenv("LINA_HOME", str(env.root))
    monkeypatch.setenv("HERMES_HOME", str(hermes_agent))
    from hermes_cli.worker import bidding
    monkeypatch.setattr(bidding, "probe_all", lambda devs, root, **_: [dict(READY)] * len(devs))
    return env


def _worker_run(tid: str, kind: str, **fields) -> None:
    """A run as the worker writes one: the hub's store under ``settings.root()``, which resolves ``lina_home()`` with
    no base handed in — the store ``lina worker submit`` saves its job file and receipts under."""
    from hermes_cli.worker import settings
    from hermes_cli.worker.hub_store import Store
    store = Store(settings.root())
    store.save({"id": tid, "bot": "alpha", "topic": f"topic-{tid}", "mode": "ulw", "state": "running", "host": None,
                "created": NOW - 60, "started": NOW - 30, "prompt": "ulw # 기기 점검\nbody\n", "verdict": None,
                "card": None, "todo": None, **fields})
    store.receipt(tid, kind)


def test_the_board_reads_the_runs_devices_and_run_events_the_worker_wrote(split_roots, monkeypatch):
    from hermes_cli.worker import hub
    env = split_roots
    monkeypatch.setattr(hub.Hub, "meta", lambda _hub, job, live=False: {"state": "running"})
    board_root = lina_board._root()
    assert board_root == lina_ocx.lina_root() != env.root  # the two roots disagree, as they do in a card session

    _worker_run("OT-ALP0001", "exec_start", host="here")
    now = float(NOW)

    runs = lina_board.worker_read(board_root, now)
    assert [run["id"] for run in runs] == ["OT-ALP0001"], "the run the worker wrote is on the board"

    errors: list[dict] = []
    devices = lina_board.devices_read(board_root, now, errors)
    assert errors == []
    assert [machine["name"] for machine in devices["machines"]] == ["here"], "the devices are the worker's own config's"
    assert devices["person"] == []

    stream = lina_board_events.Stream(board_root)
    try:
        cursor = stream.tail()
        _worker_run("OT-ALP0002", "exec_end")
        _, events, resync = stream.fetch(cursor)
    finally:
        stream.close()
    assert resync is False
    assert [(event["kind"], event["id"]) for event in events if event["source"] == "worker"] == [("exec_end",
                                                                                                "OT-ALP0002")]


def test_the_board_takes_the_worker_root_from_the_worker_s_own_rule(split_roots):
    """One helper decides it: the board asks the worker's own ``lina_home``, never a second rule of its own."""
    from hermes_cli.worker import settings
    env = split_roots
    assert lina_board._worker_base() == settings.lina_home() == env.root
