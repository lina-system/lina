"""Offline LOCAL pacing acceptance: one bind, no manual catch-up triggers."""
import sqlite3
import threading
import time

import pytest

from hermes_lcm import command, embedding_maintenance as maintenance
from hermes_lcm import embedding_provider as providers, engine as engines
from hermes_lcm.config import LCMConfig
from hermes_lcm.dag import SummaryNode
from hermes_lcm.engine import LCMEngine
from hermes_lcm.vector_store import VectorStore
from tests.test_issue_1014_incremental_embedding import FakeProvider

REAL_PREPARE = maintenance.start_local_model_prepare

# A wall-clock bound is not a pass/fail signal: on a runner slower than the bound, a test whose work
# is still coming fails. Wait on the engine's own progress instead; this cap only ends a hang, and
# reaching it fails with what is still outstanding.
WAIT_CAP_S = 300.0
POLL_S = 0.1


def _wait_for(what, pending):
    """Poll ``pending`` until it reports nothing outstanding.

    ``pending()`` returns a description of what the engine still owes, or ``""`` once its own work
    has reached the expected state. The cap never decides the check: reaching it fails with that
    description, so a hang names what never finished.
    """
    deadline = time.monotonic() + WAIT_CAP_S
    while (outstanding := pending()):
        if time.monotonic() >= deadline:
            pytest.fail(f"{what} did not finish within {WAIT_CAP_S:.0f}s: {outstanding}")
        time.sleep(POLL_S)


def _difference(kind, expected, actual):
    """``""`` when ``actual`` is exactly ``expected``, else the ids that differ on either side."""
    if actual == expected:
        return ""
    absent, extra = sorted(expected - actual), sorted(actual - expected)
    return f"{kind} ids: {len(absent)} missing (e.g. {absent[:3]}), {len(extra)} unexpected (e.g. {extra[:3]})"


def _pending_coverage(conn, summaries, chunks):
    """What the embedding meta tables still lack, or ``""`` once they hold exactly these ids."""
    embedded = {str(row[0]) for row in conn.execute("SELECT embedded_id FROM lcm_embedding_meta")}
    chunked = {str(row[0]) for row in conn.execute("SELECT chunk_id FROM lcm_chunk_meta")}
    return ", ".join(part for part in (_difference("summary", summaries, embedded),
                                       _difference("chunk", chunks, chunked)) if part)


def _pending_passes(engine):
    """Whether this engine's embedding passes are still queued or active, or ``""`` once none is."""
    return "" if engine.drain_embedding_maintenance(0) else "an embedding pass is still scheduled"


@pytest.fixture
def local_store(tmp_path, monkeypatch):
    maintenance._STOP_EMBEDDING_MAINTENANCE.clear()
    config = LCMConfig(database_path=str(tmp_path / "local.db"),
                       embeddings_enabled=True, embedding_provider="fastembed",
                       embedding_model="fixture-local", temporal_rollups_enabled=False)
    engine = LCMEngine(config=config, hermes_home=str(tmp_path / "home"))
    scheduler = engines._RollupMaintenanceScheduler(kind="incremental embedding")
    monkeypatch.setattr(engines, "_EMBEDDING_MAINTENANCE_SCHEDULER", scheduler)
    monkeypatch.setattr(providers, "probe_provider_availability", lambda _c: {"available": True})
    monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: FakeProvider(c.embedding_provider, c.embedding_model))
    store = VectorStore(config.database_path, config=config)
    store.register_profile(config.embedding_model, "fastembed", 2)
    store.register_profile(config.embedding_model, "fastembed", 2, task="chunk")
    store.close()
    yield engine, scheduler
    engine.shutdown()
    scheduler.quiesce(5)
    maintenance._STOP_EMBEDDING_MAINTENANCE.clear()


def test_one_bind_autonomously_drains_all_expected_ids(local_store):
    engine, _scheduler = local_store
    expected_summaries = set()
    expected_chunks = set()
    for i in range(300):
        engine._store.append("seed", {"role": "user", "content": f"local pacing canary {i} " + "detail " * 60})
    for i in range(65):
        expected_summaries.add(str(engine._dag.add_node(SummaryNode(
            session_id="seed", depth=0, summary=f"local pacing leaf {i}",
            created_at=float(i), latest_at=float(i)))))
    with sqlite3.connect(engine._store.db_path) as conn:
        for row in conn.execute("SELECT store_id, role, content FROM messages"):
            expected_chunks.update(c.chunk_id for c in command.chunk_message(*row, policy="conversational"))
    engine.on_session_start("bound-once", context_length=200000)
    # Wait on the bind's own embedding work reaching the expected set; the cap only ends a hang.
    with sqlite3.connect(engine._store.db_path) as conn:
        _wait_for("the autonomous embedding pass",
                  lambda: _pending_coverage(conn, expected_summaries, expected_chunks))
        actual_summaries = {str(r[0]) for r in conn.execute("SELECT embedded_id FROM lcm_embedding_meta")}
        actual_chunks = {str(r[0]) for r in conn.execute("SELECT chunk_id FROM lcm_chunk_meta")}
    assert actual_summaries == expected_summaries
    assert actual_chunks == expected_chunks


def test_complete_stops_with_visible_reason(local_store):
    engine, scheduler = local_store
    admitted = []
    schedule = scheduler.schedule

    def record(*args, **kwargs):
        admitted.append(args[0])
        return schedule(*args, **kwargs)

    # No eligible rows: an entire summary/chunk cycle must terminate visibly.
    scheduler.schedule = record
    # Read the baseline before the bind: the pass can finish before this test's next statement runs.
    before, _failures = maintenance.incremental_embedding_state(engine._store.db_path)
    engine.on_session_start("empty", context_length=200000)

    def pending():
        last, _ = maintenance.incremental_embedding_state(engine._store.db_path)
        if last == before:
            return "no incremental embedding pass finished after the bind"
        if not last[1].endswith(" complete"):
            return f"the last pass ended {last[1]!r}, not ' complete'"
        return _pending_passes(engine)

    # The pass's own recorded outcome is the completion event; the cap only ends a hang.
    _wait_for("the empty embedding cycle", pending)
    last, _ = maintenance.incremental_embedding_state(engine._store.db_path)
    assert last[1].endswith(" complete")
    count = len(admitted)
    assert engine.drain_embedding_maintenance(0)
    assert len(admitted) == count
    assert last[1] in command.handle_lcm_command("status", engine)


class Clock:
    def __init__(self):
        self.now = 1000.0

    def monotonic(self):
        return self.now

    def __getattr__(self, name):
        return getattr(time, name)


@pytest.mark.parametrize("interrupt", ["retire", "stop"])
def test_duty_wait_is_retirement_interruptible(local_store, monkeypatch, interrupt):
    engine, scheduler = local_store
    clock = Clock()
    monkeypatch.setattr(maintenance, "time", clock)
    monkeypatch.setattr(engines, "time", clock)
    # Put the paced idle wait hours out of reach of any cap this test uses: the interrupt is then the
    # only thing that can end the wait in time, so a wait that ignored it fails instead of passing late.
    monkeypatch.setattr(maintenance, "_LOCAL_IDLE_MULTIPLIER", maintenance._LOCAL_IDLE_MULTIPLIER * 10_000)
    admitted = threading.Event()
    waits = []
    observed = []
    original = scheduler.schedule
    real_pass = engines.run_incremental_embedding_pass

    def measured(*args, **kwargs):
        result = real_pass(*args, **kwargs)
        observed.append(result)
        return result

    def schedule(*args, **kwargs):
        result = original(*args, **kwargs)
        if kwargs.get("delay", 0):
            waits.append(kwargs["delay"])
            admitted.set()
        return result

    def run(*_a, **_kw):
        clock.now += 1.25
        return dict(status="complete", selected=1, embedded=1, privacy_withheld=0,
                    skipped=[], failed=[], error=None, stop_reason=None, remaining=1)

    monkeypatch.setattr(command, "_embedding_backfill_summary_run", run)
    monkeypatch.setattr(scheduler, "schedule", schedule)
    monkeypatch.setattr(engines, "run_incremental_embedding_pass", measured)
    engine.on_session_start("duty", context_length=200000)
    assert admitted.wait(2), "no paced continuation was admitted"
    assert waits[0] >= maintenance._LOCAL_IDLE_MULTIPLIER * 1.25
    # Hours away, so the wait below cannot end by the delay running out: only the interrupt ends it.
    assert waits[0] > WAIT_CAP_S * 10
    assert observed[0]["delay"] == pytest.approx(maintenance._LOCAL_IDLE_MULTIPLIER * observed[0]["active_s"])
    assert observed[0]["vectors_written"] == observed[0]["rows_written"] == 1
    assert observed[0]["overrun_s"] == 0.25
    assert observed[0]["remaining"] is True
    assert observed[0]["scan_frontier_advance"] is False
    if interrupt == "retire":
        engine._retire_embedding_maintenance(wait=True)
    else:
        maintenance._STOP_EMBEDDING_MAINTENANCE.set()
    # The paced continuation (hours away) was never admitted: the interrupt ends the duty wait instead
    # of riding out the idle delay and running another pass. The scheduler's own state is the signal,
    # and the wait above only ends inside the cap if the interrupt is what ended it.
    _wait_for("the duty wait to end", lambda: _pending_passes(engine))
    assert len(observed) == 1
    assert engine.drain_embedding_maintenance(0)
    assert not scheduler._queued_keys


def test_restart_resumes_persisted_coverage(local_store, monkeypatch):
    engine, _scheduler = local_store
    entered, release = threading.Event(), threading.Event()
    for i in range(150):
        engine._store.append("seed", {"role": "user", "content": f"restart canary {i} " + "detail " * 60})
        engine._dag.add_node(SummaryNode(session_id="seed", depth=0, summary=f"restart leaf {i}",
                                        created_at=float(i), latest_at=float(i)))

    class Paused(FakeProvider):
        def embed_document_batches(self, texts, **kwargs):
            if texts[0].startswith("restart canary"):
                entered.set()
                assert release.wait(5)
            yield from super().embed_document_batches(texts, **kwargs)

    monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: Paused(c.embedding_provider, c.embedding_model))
    successor = None
    try:
        engine.on_session_start("before-restart", context_length=200000)
        assert entered.wait(3)
        engine._retire_embedding_maintenance(wait=False)
        release.set()
        engine._retire_embedding_maintenance(wait=True)
        with sqlite3.connect(engine._store.db_path) as conn:
            before = {r[0] for r in conn.execute("SELECT embedded_id FROM lcm_embedding_meta")}
            assert 0 < len(before) < 150
        monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: FakeProvider(c.embedding_provider, c.embedding_model))
        successor = LCMEngine(config=engine._config, hermes_home=str(engine._hermes_home))
        successor.on_session_start("after-restart", context_length=200000)
        # Wait on the successor's own embedding work reaching the expected set, not on a drain bound:
        # a slow runner must not decide this. The cap only ends a hang and names what is missing.
        with sqlite3.connect(engine._store.db_path) as conn:
            expected_summaries = {str(r[0]) for r in conn.execute("SELECT node_id FROM summary_nodes WHERE depth=0")}
            expected_chunks = {c.chunk_id for row in conn.execute("SELECT store_id, role, content FROM messages")
                               for c in command.chunk_message(*row, policy="conversational")}
            _wait_for("the successor's embedding pass",
                      lambda: _pending_coverage(conn, expected_summaries, expected_chunks))
            summaries = {r[0] for r in conn.execute("SELECT embedded_id FROM lcm_embedding_meta")}
            chunks = {r[0] for r in conn.execute("SELECT chunk_id FROM lcm_chunk_meta")}
        assert summaries == expected_summaries and before <= summaries
        assert chunks == expected_chunks
    finally:
        release.set()
        if successor is not None:
            successor.shutdown()


def test_retired_prepare_never_registers_store(local_store, monkeypatch):
    engine, _scheduler = local_store
    entered, release = threading.Event(), threading.Event()
    writes = []

    class Model(FakeProvider):
        def warmup(self):
            entered.set()
            assert release.wait(5)
            return [1.0, 0.0]

    register = VectorStore.register_profile

    def recorded(*args, **kwargs):
        writes.append(str(args[0].db_path))
        return register(*args, **kwargs)

    monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: Model(c.embedding_provider, c.embedding_model))
    monkeypatch.setattr(VectorStore, "register_profile", recorded)
    try:
        with pytest.MonkeyPatch.context() as prepare_patch:
            prepare_patch.setattr(maintenance, "start_local_model_prepare", REAL_PREPARE)
            engine._schedule_embedding_maintenance()
        assert entered.wait(3)
        engine._retire_embedding_maintenance(wait=True)
        release.set()
        maintenance._PREPARE_THREAD.join(3)
        assert not maintenance._PREPARE_THREAD.is_alive()
        assert writes == []
    finally:
        release.set()
        thread = maintenance._PREPARE_THREAD
        if thread is not None:
            thread.join(3)


@pytest.mark.parametrize("recover", [False, True])
def test_prepare_retries_are_bounded_and_can_recover(local_store, monkeypatch, recover):
    engine, _scheduler = local_store
    clock = Clock()
    attempts = []

    class Model(FakeProvider):
        def warmup(self):
            attempts.append(clock.now)
            if recover and len(attempts) == 2:
                return [1.0, 0.0]
            raise RuntimeError("offline fixture model unavailable")

    monkeypatch.setattr(maintenance, "time", clock)
    monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: Model(c.embedding_provider, c.embedding_model))
    limit = 2 if recover else 6
    for i in range(limit):
        assert REAL_PREPARE(engine._store.db_path, engine._config)
        maintenance._PREPARE_THREAD.join(3)
        assert not REAL_PREPARE(engine._store.db_path, engine._config)
        clock.now += 600.0
    if recover:
        assert maintenance.local_model_state(engine._config) == ("present", "")
    else:
        assert not REAL_PREPARE(engine._store.db_path, engine._config)
    assert len(attempts) == limit
    assert all(b - a >= 600.0 for a, b in zip(attempts, attempts[1:]))


def test_store_registration_failure_does_not_poison_model(local_store, monkeypatch):
    engine, _scheduler = local_store

    class Model(FakeProvider):
        def warmup(self):
            return [1.0, 0.0]

    monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: Model(c.embedding_provider, c.embedding_model))
    monkeypatch.setattr(command, "_embedding_register_profiles", lambda *_a, **_k: "error: fixture store registration failed")
    assert REAL_PREPARE(engine._store.db_path, engine._config)
    maintenance._PREPARE_THREAD.join(3)
    assert maintenance.local_model_state(engine._config) == ("present", "")
    peer = LCMConfig(embeddings_enabled=True, embedding_provider="fastembed", embedding_model="fixture-local")
    assert maintenance.local_model_state(peer) == ("present", "")
    monkeypatch.setattr(command, "_embedding_current_profile", lambda _c: None)
    outcome = maintenance.run_incremental_embedding_pass(engine._store.db_path, engine._config)
    assert outcome == "auto_register_unavailable"
    assert maintenance.local_model_state(peer) == ("present", "")


def test_failing_row_is_not_rewritten_on_each_bind(local_store, monkeypatch):
    engine, _scheduler = local_store
    calls = []

    class Failing(FakeProvider):
        def embed_document_batches(self, texts, *, before_dispatch):
            calls.append(tuple(texts))
            before_dispatch(tuple(range(len(texts))))
            raise providers.EmbeddingProviderError("fixture row refuses embedding")
            yield

    monkeypatch.setattr(command, "resolve_provider", lambda c, **_k: Failing(c.embedding_provider, c.embedding_model))
    engine._dag.add_node(SummaryNode(session_id="seed", depth=0, summary="permanent row failure",
                                    created_at=1.0, latest_at=1.0))
    engine.on_session_start("first", context_length=200000)
    # The first pass must finish (and record the row as inflight) before the second bind: wait on it.
    _wait_for("the first bind's embedding pass", lambda: _pending_passes(engine))
    assert len(calls) == 1
    with sqlite3.connect(engine._store.db_path) as conn:
        before = conn.execute("SELECT embedded_id, updated_at FROM lcm_embedding_backfill_inflight").fetchall()
    first_pass, _ = maintenance.incremental_embedding_state(engine._store.db_path)
    engine.on_session_start("second", context_length=200000)
    # The second bind's own recorded pass is the event: it re-scans the corpus, finds the row inflight
    # and leaves it alone. (Idle is the wrong signal here: the engine keeps a row backoff queued.)
    _wait_for("the second bind's embedding pass",
              lambda: "" if maintenance.incremental_embedding_state(engine._store.db_path)[0] != first_pass
              else "no embedding pass finished after the second bind")
    engine._retire_embedding_maintenance(wait=True)
    with sqlite3.connect(engine._store.db_path) as conn:
        after = conn.execute("SELECT embedded_id, updated_at FROM lcm_embedding_backfill_inflight").fetchall()
    assert len(calls) == 1
    assert before == after


@pytest.mark.parametrize("retire_ready", [False, True])
def test_queue_re_admits_65th_database(retire_ready):
    scheduler = engines._RollupMaintenanceScheduler(kind="incremental embedding")
    ran = []
    try:
        with scheduler._condition:
            accepted = [scheduler.schedule((str(i), "embeddings"), lambda i=i: ran.append(i), owner=i + 1)
                        for i in range(65)]
            assert len(scheduler._jobs) <= 64
            if retire_ready:
                for i in range(64):
                    assert scheduler.retire_owner(i + 1, timeout=0)
        assert all(accepted), "overflow database was dropped"
        assert scheduler.drain({(str(i), "embeddings") for i in range(65)}, 3)
        assert set(ran) == ({64} if retire_ready else set(range(65)))
    finally:
        scheduler.quiesce(3)


def test_default_bge_uses_one_intra_op_thread(monkeypatch, tmp_path):
    constructions = []

    class Model:
        def __init__(self, **kwargs):
            constructions.append(kwargs)

        def query_embed(self, texts):
            return [[1.0, 0.0] for _ in texts]

    monkeypatch.setattr(providers, "_load_fastembed", lambda: Model)
    provider = providers.FastembedProvider("BAAI/bge-small-en-v1.5", cache_dir=tmp_path)
    assert provider.embed_query("thread canary") == [1.0, 0.0]
    assert provider._cache_key()[-1] == constructions[0]["threads"] == 1


@pytest.mark.parametrize("blocked", ["lease_held", "circuit_open"])
def test_transient_block_is_requeued(local_store, monkeypatch, blocked):
    engine, scheduler = local_store
    admitted = threading.Event()
    delays = []
    real_schedule = scheduler.schedule

    def schedule(*args, **kwargs):
        result = real_schedule(*args, **kwargs)
        if kwargs.get("delay", 0):
            delays.append(kwargs["delay"])
            admitted.set()
        return result

    class Breaker:
        def is_open(self):
            return True

    if blocked == "lease_held":
        monkeypatch.setattr(command, "_embedding_backfill_summary_run",
                            lambda *_a, **_k: command._BackfillRefusal("fixture lease contention", reason="lease_held"))
    else:
        engine._lcm_embedding_provider_cache = (None, type("Cached", (), {"breaker": Breaker()})())
    monkeypatch.setattr(scheduler, "schedule", schedule)
    engine.on_session_start("transient", context_length=200000)
    assert admitted.wait(2), "transient block was dropped"
    assert delays[0] >= 1.0
    assert not engine.drain_embedding_maintenance(0)
    engine._retire_embedding_maintenance(wait=True)
    last, _ = maintenance.incremental_embedding_state(engine._store.db_path)
    assert last[1] == blocked
