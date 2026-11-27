"""Compare a single Rust connection to both real lexical entry points (lina#928)."""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import time
from contextlib import ExitStack, closing
from dataclasses import dataclass
from pathlib import Path

import pytest

from hermes_cli.external_messages import disabled_sources, enabled
from hermes_state_store import MemoryRow, MessageStore
from plugins.memory.lina.search import MemorySearch, Readers
from plugins.memory.lina import links
from plugins.memory.lina.identity import IdentityRegistry
from tests._fixtures.memory_store import _connect
from .fixtures import CASES

pytestmark = pytest.mark.skipif(shutil.which("cargo") is None, reason="store reader parity requires Cargo on PATH")


def _fingerprints(rows: list[MemoryRow]) -> list[dict[str, str]]:
    result = []
    for row in rows:
        assert row.content is not None
        result.append({"uid": row.uid, "content_hash": hashlib.sha256(row.content.encode()).hexdigest()})
    return result


@dataclass
class Observation:
    request: dict
    expected: list[dict[str, str]]


class ObservedStore(MessageStore):
    """Keep the ranked rows received by each lexical leg before the tool fuses them."""

    def __init__(self, agent_id: str, dsn: str):
        super().__init__(agent_id, dsn=dsn)
        self.observations: list[Observation] = []

    def search_memory(self, query, *, sources, limit=20, authors=None, speakers=None):
        args = dict(query=query, sources=sources, limit=limit, authors=authors, speakers=speakers)
        rows = super().search_memory(**args)
        self.observations.append(Observation(
            dict(agent_id=self.agent_id, disabled_sources=disabled_sources(), **args), _fingerprints(rows),
        ))
        return rows


def _rust_batch(dataset, observations, tmp_path):
    target = Path(os.environ.get("CARGO_TARGET_DIR", tmp_path / "target"))
    root = Path(__file__).resolve().parents[3]
    subprocess.run(
        [dataset.cargo, "build", "--locked", "-p", "lina-store-reader", "--target-dir", target],
        cwd=root, check=True, timeout=600,
    )
    binary = target / "debug" / ("lina-store-reader.exe" if os.name == "nt" else "lina-store-reader")
    # Each disconnected backend increments sessions. This fixture owns the
    # database and keeps the witness connected throughout the Rust batch.
    with _connect(dataset.dsn) as witness:
        sessions = "SELECT sessions FROM pg_stat_database WHERE datname = current_database()"
        before = witness.execute(sessions).fetchone()[0]
        process = subprocess.run(
            [binary], input=json.dumps([o.request for o in observations]), capture_output=True,
            text=True, timeout=120, env={**os.environ, "LINA_STORE_DSN": dataset.dsn}, check=False,
        )
        assert process.returncode == 0, f"reader exited {process.returncode}: {process.stderr.strip()}"
        opened = witness.execute(sessions).fetchone()[0] - before
        assert opened == 1, f"Rust opened {opened} PostgreSQL sessions for one batch"
    return json.loads(process.stdout)


def test_order_content_and_visibility_across_reused_connection(parity_store, tmp_path, record_property):
    dataset = parity_store
    assert enabled(), "the lexical messages leg must run"
    record_property("timezone", f"{os.environ['TZ']} {time.strftime('%Z %z')}")
    observations = []
    labels = []
    summary = {}
    with ExitStack() as stack:
        registry = stack.enter_context(closing(IdentityRegistry()))
        stores = {role: stack.enter_context(closing(ObservedStore(agent, dataset.dsn)))
                  for role, agent in dataset.agents.items()}
        def person_refs(person):
            return links.row_refs(registry.handles(links.person_id(registry, person)))

        for case in CASES:
            lexical = []
            for role in ("bot_a", "bot_b", "bot_a", "missing_owner", "bot_b", "owner"):
                store = stores[role]
                store.observations.clear()
                args = dict(sources=case.sources, limit=case.limit, authors=case.authors, speakers=case.speakers)
                person = dataset.people["owner"] if case.name == "person-filter" else None
                if person:
                    args["authors"] = person_refs(person)
                rows = store.search_memory(case.query, **args)
                assert tuple(r.uid for r in rows) == case.expected_uids[role], (role, case.name)
                if case.expected_content is not None and rows:
                    assert rows[0].content == case.expected_content, (role, case.name)

                search = MemorySearch(Readers(store=lambda store=store: store, person_refs=person_refs))
                tool = search.search(case.query, sources=["agent", "messages"], limit=case.limit,
                                     speakers=case.speakers, senders=case.authors, person=person)
                assert not tool["skipped"], (role, case.name, "lexical source skipped")
                direct, *legs = store.observations
                assert legs, (role, case.name, "no lexical observations")
                lexical_uids = {row["uid"] for leg in legs for row in leg.expected}
                assert all(hit["ref"].removeprefix("msg:") in lexical_uids for hit in tool["results"])
                assert bool(tool["results"]) == bool(lexical_uids)
                summary[role, case.name] = [len(direct.expected), sum(len(o.expected) for o in legs)]
                labels.append((role, case.name, "store"))
                observations.append(direct)
                lexical.extend(((role, case.name, "lexical"), leg) for leg in legs)
            # Exercise A, B, A, missing owner, B on consecutive physical checkouts.
            for label, leg in lexical:
                labels.append(label)
                observations.append(leg)

    results = _rust_batch(dataset, observations, tmp_path)
    for label, obs, actual in zip(labels, observations, results, strict=True):
        role = label[0]
        foreign = set().union(*(ids for other, ids in dataset.private_uids.items() if other != role))
        assert not any(row["uid"] in foreign for row in actual), (*label, "foreign private UID")
        assert actual == obs.expected, (*label, "ordered UID/content hash mismatch")

    with ExitStack() as stack:
        fresh = {agent: stack.enter_context(closing(MessageStore(agent, dsn=dataset.dsn)))
                 for agent in dataset.agents.values()}
        for obs, label in zip(observations, labels, strict=True):
            if label[2] == "store":
                args = dict(obs.request)
                store = fresh[args.pop("agent_id")]
                del args["disabled_sources"]
                assert _fingerprints(store.search_memory(**args)) == obs.expected, (*label, "switch-back")

    record_property("parity_matrix", json.dumps([
        {"role": role, "case": name, "direct": counts[0], "lexical": counts[1]}
        for (role, name), counts in summary.items()
    ]))
    record_property("matched_requests", len(results))
    record_property("rust_sessions", 1)
    record_property("foreign_private_rows", 0)
