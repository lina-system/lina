"""Device bidding of ``lina worker`` (lina#35): fill ratio first, a ``fast`` device wins a tie, ``no_repo`` refuses
repo work, ``last`` only when the others are full, and nothing ready means the queue."""

from hermes_cli.worker import bidding
from hermes_cli.worker.settings import Device


def probe(running=0, load=0.0, cores=8, **extra):
    return {"ready": True, "serve": True, "running": running, "cores": cores, "load": load, "free_gb": 50,
            "repo": True, **extra}


def test_the_least_filled_device_wins():
    devices = [Device("a", "a", cap=2), Device("b", "b", cap=4)]
    winner, rows = bidding.choose(devices, [probe(running=1), probe(running=1)], repo=None)
    assert winner.name == "b"
    assert [r["score"][0] for r in rows] == [0.5, 0.25]


def test_a_fast_device_wins_a_tie_on_fill():
    devices = [Device("slow", "slow", cap=2), Device("fast", "fast", cap=2, fast=True)]
    winner, _ = bidding.choose(devices, [probe(running=1, load=0.0), probe(running=1, load=7.0)], repo=None)
    assert winner.name == "fast"


def test_no_repo_refuses_repo_work_but_takes_other_work():
    devices = [Device("docs", "docs", cap=4, no_repo=True), Device("build", "build", cap=1)]
    probes = [probe(), probe(running=0, load=6.0)]
    winner, rows = bidding.choose(devices, probes, repo="owner/name")
    assert winner.name == "build" and rows[0]["why"] == "takes no repo work"
    assert bidding.choose(devices, probes, repo=None)[0].name == "docs"


def test_last_wins_only_when_every_other_device_is_full():
    devices = [Device("main", "main", cap=1), Device("spare", "spare", cap=4, last=True)]
    winner, rows = bidding.choose(devices, [probe(running=0), probe(running=0)], repo=None)
    assert winner.name == "main" and rows[1]["why"] == "only when the others are full"
    assert bidding.choose(devices, [probe(running=1), probe(running=0)], repo=None)[0].name == "spare"


def test_nothing_ready_means_the_queue():
    devices = [Device("a", "a", cap=1), Device("b", "b", cap=1), Device("c", "c", cap=1)]
    winner, rows = bidding.choose(devices, [probe(running=1), probe(ready=False, why="omo daemon not ready"),
                                            {"error": "ssh: timeout"}], repo=None)
    assert winner is None
    assert [r["why"] for r in rows] == ["full", "omo daemon not ready", "unreachable: ssh: timeout"]

