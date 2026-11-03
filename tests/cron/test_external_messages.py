"""Host ingestion scheduling contracts for #18; synthetic spools and virtual time."""
from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path
from types import ModuleType
from unittest.mock import Mock

import pytest

from cron import external_messages as chore
from cron import scheduler_provider as provider


@pytest.fixture
def ingestion(tmp_path, monkeypatch):
    """Only replace the lead-owned ingestion boundary, not the scheduling or locks."""
    import hermes_constants

    spool = tmp_path / "spool.jsonl"
    spool.write_bytes(b'{"synthetic":true}\n')
    api = ModuleType("hermes_cli.external_messages")
    api.configured_spool = Mock(return_value=spool)
    api.ingest = Mock(return_value={"lines": 1, "inserted": 1, "offset": spool.stat().st_size})
    monkeypatch.setitem(sys.modules, "hermes_cli.external_messages", api)
    monkeypatch.setattr(hermes_constants, "get_default_hermes_root", lambda: tmp_path)
    monkeypatch.setattr(chore, "time", type("Clock", (), {"time": staticmethod(lambda: 1000.0)}))
    return spool, api


def test_unchanged_spool_skips_ingestion_across_due_checks(ingestion, monkeypatch):
    # Given a successfully ingested spool and a later host interval.
    _, api = ingestion
    chore.maybe_ingest()
    monkeypatch.setattr(chore.time, "time", lambda: 1300.0)
    # When the host checks the unchanged spool again.
    chore.maybe_ingest()
    # Then the successful stat guard avoids another read.
    assert api.ingest.call_count == 1


@pytest.mark.parametrize("change", ["append", "rewrite", "shrink"])
def test_changed_spool_ingests_at_next_host_interval(ingestion, monkeypatch, change):
    # Given a previous signature and an explicit size or nanosecond mtime change.
    spool, api = ingestion
    chore.maybe_ingest()
    before = spool.stat()
    content = {"append": b"more synthetic data\n" + spool.read_bytes(),
               "rewrite": b"x" * before.st_size, "shrink": b""}[change]
    spool.write_bytes(content)
    os.utime(spool, ns=(before.st_atime_ns, before.st_mtime_ns + 1))
    monkeypatch.setattr(chore.time, "time", lambda: 1299.0)
    chore.maybe_ingest()
    assert api.ingest.call_count == 1
    monkeypatch.setattr(chore.time, "time", lambda: 1300.0)
    # When the next interval starts, including exact boundary equality.
    chore.maybe_ingest()
    # Then even same-size rewrites and truncations reach the ingestion API.
    assert api.ingest.call_count == 2
    assert api.ingest.call_args.args == (spool,)


def test_failed_ingestion_does_not_cache_spool_signature(ingestion, monkeypatch, caplog):
    # Given an ingestion failure before any successful read.
    _, api = ingestion
    api.ingest.side_effect = OSError("synthetic failure")
    chore.maybe_ingest()
    api.ingest.side_effect = None
    monkeypatch.setattr(chore.time, "time", lambda: 1299.0)
    chore.maybe_ingest()
    assert api.ingest.call_count == 1
    monkeypatch.setattr(chore.time, "time", lambda: 1300.0)
    # When ingestion recovers with the exact same spool stat.
    chore.maybe_ingest()
    # Then the failure did not mark the spool successfully ingested.
    assert api.ingest.call_count == 2
    assert any(record.exc_info for record in caplog.records)


def test_unconfigured_spool_does_not_create_host_state(ingestion, tmp_path):
    # Given no root-configured spool.
    _, api = ingestion
    api.configured_spool.return_value = None
    # When the ticker checks for external messages.
    chore.maybe_ingest()
    # Then the chore stays inert without creating a host store.
    api.ingest.assert_not_called()
    assert not (tmp_path / "external_messages").exists()


def test_contended_host_lock_prevents_ingestion(ingestion, tmp_path):
    from gateway.status import _release_file_lock, _try_acquire_file_lock

    # Given another lock handle already owns the host ingestion lock.
    _, api = ingestion
    directory = tmp_path / "external_messages"
    directory.mkdir()
    with (directory / ".ticker.lock").open("a+", encoding="utf-8") as other:
        assert _try_acquire_file_lock(other)
        try:
            # When a ticker tries to ingest the same root.
            chore.maybe_ingest()
            # Then no ingestion runs while the host lock is held.
            api.ingest.assert_not_called()
        finally:
            _release_file_lock(other)
    chore.maybe_ingest()
    assert api.ingest.call_count == 1


def test_another_process_cannot_ingest_during_host_ownership(ingestion, tmp_path):
    from gateway.status import _release_file_lock, _try_acquire_file_lock

    # Given a host lock held by this process and a separate gateway contender.
    directory = tmp_path / "external_messages"
    directory.mkdir()
    lock_path = directory / ".ticker.lock"
    command = (
        "import sys; from pathlib import Path; "
        "from cron.external_messages import _ingest_if_due; "
        "import hermes_constants; import types; "
        "api = types.ModuleType('hermes_cli.external_messages'); "
        "api.configured_spool = lambda: Path(sys.argv[1]); "
        "api.ingest = lambda spool: print('INGESTED'); "
        "sys.modules[api.__name__] = api; "
        "hermes_constants.get_default_hermes_root = lambda: Path(sys.argv[2]); "
        "_ingest_if_due()"
    )
    spool, _ = ingestion
    with lock_path.open("a+", encoding="utf-8") as owner:
        assert _try_acquire_file_lock(owner)
        try:
            # When a separate process executes the real ingestion guard.
            result = subprocess.run(
                [sys.executable, "-c", command, str(spool), str(tmp_path)],
                capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=10, check=True,
            )
        finally:
            _release_file_lock(owner)
    # Then it never enters ingestion or publishes a successful host signature.
    assert result.stdout == ""
    assert not (directory / "ticker.json").exists()


@pytest.mark.parametrize("multiplex", [False, True])
def test_real_ticker_checks_once_per_host_cycle(ingestion, tmp_path, monkeypatch, multiplex):
    import cron.jobs
    import cron.scheduler
    import hermes_constants

    # Given a real ticker, two profiles, and eleven cycles on a virtual clock.
    spool, api = ingestion
    homes = [tmp_path / "a", tmp_path / "b"]
    for home in homes:
        home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(homes[0]))
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    clock = type("Clock", (), {"now": 1000.0})()
    monkeypatch.setattr(provider, "time", type("TickerClock", (), {
        "monotonic": staticmethod(lambda: clock.now),
    }))
    monkeypatch.setattr(chore.time, "time", lambda: clock.now)
    checked_homes = []
    api.configured_spool.side_effect = lambda: (
        checked_homes.append(hermes_constants.get_hermes_home()) or spool
    )
    ingested_at = []
    api.ingest.side_effect = lambda path: ingested_at.append(clock.now)
    ticks = []
    monkeypatch.setattr(cron.scheduler, "tick", lambda **kwargs: ticks.append(clock.now))
    monkeypatch.setattr(cron.jobs, "record_ticker_heartbeat", lambda **kwargs: None)
    monkeypatch.setattr(cron.jobs, "clear_ticker_error", lambda: None)
    scheduler = provider.InProcessCronScheduler()
    monkeypatch.setattr(scheduler, "recover_interrupted", lambda: 0)

    class Stop:
        cycles = 0

        def is_set(self):
            return self.cycles == 11

        def wait(self, timeout):
            self.cycles += 1
            clock.now += timeout
            # Model the collector appending once every host interval.
            if self.cycles in (5, 10):
                with spool.open("ab") as stream:
                    stream.write(b"synthetic append\n")

    # When both real loop variants execute on the virtual clock without sleeping.
    scheduler.start(Stop(), profile_homes=[("a", homes[0]), ("b", homes[1])] if multiplex else None)
    # Then ingestion is independent of profile count and runs outside their scopes.
    assert ingested_at == [1000.0, 1300.0, 1600.0]
    assert checked_homes == [homes[0]] * 11
    assert len(ticks) == 11 * (2 if multiplex else 1)
