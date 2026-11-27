"""Disposable pgvector store and synthetic cases for the reader parity matrix."""

from __future__ import annotations

import re
import subprocess
from collections.abc import Callable, Iterator, Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from hashlib import sha256
from types import MappingProxyType
from typing import Final

import pytest

from tests._fixtures import memory_store

_AGENTS: Final = MappingProxyType({
    "owner": "parity-owner",
    "bot_a": "parity-bot-a",
    "bot_b": "parity-bot-b",
    "missing_owner": "parity-missing-owner",
})
_PEOPLE: Final = MappingProxyType({role: f"person:{_AGENTS[role]}" for role in ("owner", "bot_a", "bot_b")})
_ALIASES: Final = MappingProxyType({
    "owner": "owner",
    "owner_gateway": "kakao:parity-owner",
    "conflict": "kakao:parity-conflict",
    "candidate": "kakao:parity-candidate",
    "undone": "kakao:parity-undone",
})
_SHARED: Final = MappingProxyType({
    "external": "messages:kakao:parity-room:shared",
    "edit_original": "messages:kakao:parity-room:edited",
    "edit_latest": "messages:kakao:parity-room:edited#v2",
    "confirmed_sender": "messages:kakao:parity-room:confirmed-sender",
    "candidate_sender": "messages:kakao:parity-room:candidate-sender",
    "hidden_mail": "messages:mail:parity-room:sample",
    "hidden_slack": "messages:slack:parity-room:sample",
})
_VISIBLE_TEXT: Final = "parityvisibility parityforgotten parityinjected"
_EDITED_TEXT: Final = "parityedit corrected shipment"
_INSTRUCTION: Final = "File the parityprojection receipt."
_KOREAN_TEXT: Final = "합성 회의록을 보관했습니다."
_AGENT_SOURCE: Final = ("agent",)
_SHARED_SOURCE: Final = ("messages",)
_BOTH_SOURCES: Final = _AGENT_SOURCE + _SHARED_SOURCE
_RECENT: Final = datetime.now(UTC) + timedelta(days=1)


@dataclass(frozen=True, slots=True)
class SearchCase:
    """Search request and independently specified, ordered result UIDs."""

    name: str
    query: str
    sources: tuple[str, ...]
    expected_uids: Mapping[str, tuple[str, ...]]
    limit: int = 20
    authors: tuple[str, ...] | None = None
    speakers: tuple[str, ...] | None = None
    expected_content: str | None = None


@dataclass(frozen=True, slots=True)
class ParityStore:
    dsn: str = field(repr=False)
    cargo: str
    agents: Mapping[str, str]
    people: Mapping[str, str]
    aliases: Mapping[str, str]
    documents: Mapping[str, str]
    private_uids: Mapping[str, frozenset[str]]


@dataclass(frozen=True, slots=True)
class _Message:
    name: str
    content: str
    at: str | datetime = "2020-01-01T00:00:00Z"
    forgotten_at: str | None = None
    author: str | None = None
    platform: str = "kakao"


_PRIVATE: Final = (
    _Message("private", _VISIBLE_TEXT),
    _Message("forgotten", "parityforgotten", forgotten_at="2020-01-03T00:00:00Z"),
    _Message("injected", "[System note: parityinjected]"),
    _Message("projection",
             '[IMPORTANT: The user has invoked the "parity" skill. The full skill content is loaded below.]'
             "\n\nparityscaffold\n\n"
             f"The user has provided the following instruction alongside the skill invocation: {_INSTRUCTION}"),
    _Message("korean", _KOREAN_TEXT),
    # The recency floor is the same for both dates. The newer hit sorts last by UID and was inserted first.
    _Message("tie_newer", "paritytie", at="2020-01-02T00:00:00Z"),
    _Message("tie_older", "paritytie"),
    _Message("wildcards", "literal percent and underscore: %_"),
    _Message("wildcard_percent_decoy", "percent by itself: %x"),
    _Message("wildcard_underscore_decoy", "underscore by itself: x_"),
    _Message("cap_first", "paritycap1"),
    _Message("cap_ninth", "paritycap9", at="2020-01-02T00:00:00Z"),
    # The two focused rows isolate recency. The newest digest still loses on text relevance.
    _Message("rank_short_new", "parityrank", at=_RECENT),
    _Message("rank_short_old", "parityrank"),
    _Message("rank_digest", "parityrank\n" + ("synthetic archive filler " * 8192), at=_RECENT + timedelta(days=1)),
)
_EXTERNAL: Final = (
    _Message("external", _VISIBLE_TEXT, at="2020-01-02T00:00:00Z", author=_ALIASES["conflict"]),
    _Message("edit_original", "parityedit original shipment", author=_ALIASES["conflict"]),
    _Message("edit_latest", _EDITED_TEXT, at="2020-01-02T00:00:00Z", author=_ALIASES["conflict"]),
    _Message("confirmed_sender", "paritysender reported the synthetic shipment", author=_ALIASES["conflict"]),
    _Message("candidate_sender", "paritysender", at="2020-01-02T00:00:00Z", author=_ALIASES["candidate"]),
    _Message("hidden_mail", "parityvisibility", platform="mail"),
    _Message("hidden_slack", "parityvisibility", platform="slack"),
)
_PRIVATE_SUFFIX: Final = {"tie_newer": "tie-z-newer", "tie_older": "tie-a-older"}
_ROLE_DOCUMENTS: Final = {
    role: {row.name: f"agent:{agent}:{_PRIVATE_SUFFIX.get(row.name, row.name)}" for row in _PRIVATE}
    for role, agent in _AGENTS.items()
}
_DOCUMENTS: Final = MappingProxyType({
    **_SHARED,
    **_ROLE_DOCUMENTS["owner"],
    **{role: documents["private"] for role, documents in _ROLE_DOCUMENTS.items()},
    **{f"{role}:{name}": uid for role, documents in _ROLE_DOCUMENTS.items() for name, uid in documents.items()},
})
_PRIVATE_UIDS: Final = MappingProxyType({
    role: frozenset(documents.values()) for role, documents in _ROLE_DOCUMENTS.items()
})
_SPEAKERS: Final = {"owner": "owner", "bot_a": "agent", "bot_b": "agent", "missing_owner": "unknown"}


def _expected(*names: str, owner_only: bool = False) -> Mapping[str, tuple[str, ...]]:
    """Resolve named outcomes from the fixture input UIDs independently of either reader."""
    return MappingProxyType({
        role: tuple(_SHARED[name] if name in _SHARED else documents[name] for name in names)
        if not owner_only or role == "owner" else ()
        for role, documents in _ROLE_DOCUMENTS.items()
    })


CASES: Final = (
    SearchCase("visibility", "parityvisibility", _BOTH_SOURCES, _expected("external", "private")),
    SearchCase("tie", "paritytie", _AGENT_SOURCE, _expected("tie_newer", "tie_older")),
    SearchCase("edited", "parityedit", _SHARED_SOURCE, _expected("edit_latest"), expected_content=_EDITED_TEXT),
    SearchCase("forgotten", "parityforgotten", _AGENT_SOURCE, _expected("private")),
    SearchCase("injected", "parityinjected", _AGENT_SOURCE, _expected("private")),
    SearchCase("projection", "parityprojection", _AGENT_SOURCE, _expected("projection"),
               expected_content=_INSTRUCTION),
    SearchCase("projection-scaffold", "parityscaffold", _AGENT_SOURCE, _expected()),
    SearchCase("author-filter", "parityvisibility", _BOTH_SOURCES, _expected("private", owner_only=True),
               limit=1, authors=(_ALIASES["owner_gateway"],)),
    SearchCase("sender-filter", "paritysender", _SHARED_SOURCE, _expected("confirmed_sender"),
               limit=1, authors=(_ALIASES["conflict"],)),
    SearchCase("person-filter", "paritysender", _SHARED_SOURCE, _expected("confirmed_sender"), limit=1),
    SearchCase("speaker-filter", "parityvisibility", _BOTH_SOURCES, _expected("private", owner_only=True),
               limit=1, speakers=("owner",)),
    SearchCase("source-filter", "parityvisibility", _AGENT_SOURCE, _expected("private"), limit=1),
    SearchCase("korean", "회의", _AGENT_SOURCE, _expected("korean"), expected_content=_KOREAN_TEXT),
    SearchCase("literal-wildcards", "%_", _AGENT_SOURCE, _expected("wildcards")),
    SearchCase("repeated-terms", " ".join(["paritycap1"] * 9 + ["paritycap9"]), _AGENT_SOURCE,
               _expected("cap_ninth", "cap_first")),
    SearchCase("term-cap", " ".join(f"paritycap{index}" for index in range(1, 13)), _AGENT_SOURCE,
               _expected("cap_first")),
    SearchCase("rank", "parityrank", _AGENT_SOURCE, _expected("rank_short_new", "rank_short_old", "rank_digest")),
)


def _seed(admin_dsn: str) -> None:
    """Insert the same input rows both implementations will read as lina_app."""
    rows = (
        *(
            (_ROLE_DOCUMENTS[role][row.name], agent, role, row)
            for role, agent in _AGENTS.items()
            for row in _PRIVATE
        ),
        *((_SHARED[row.name], None, None, row) for row in _EXTERNAL),
    )
    owner, bot_a, bot_b = (_PEOPLE[role] for role in ("owner", "bot_a", "bot_b"))
    with memory_store._connect(admin_dsn) as conn, conn.transaction(), conn.cursor() as cur:
        cur.executemany("INSERT INTO memory.identities (identity_id, kind) VALUES (%s, %s)", [
            *((person, "person") for person in _PEOPLE.values()),
            *((f"agent:{agent}", "agent") for agent in _AGENTS.values()),
        ])
        cur.executemany(
            "INSERT INTO memory.identity_keys (kind, source, key, identity_id) VALUES (%s, %s, %s, %s)", [
                *(("agent", "profile", agent, f"agent:{agent}") for agent in _AGENTS.values()),
                ("person", "local_user", _ALIASES["owner"], owner),
                ("person", "gateway_user", _ALIASES["owner_gateway"], owner),
                *(("person", "messenger_author", _ALIASES[key], bot_b)
                  for key in ("conflict", "candidate", "undone")),
            ],
        )
        cur.executemany(
            "INSERT INTO memory.identity_links (source, key, person_id, status, evidence)"
            " VALUES ('messenger_author', %s, %s, %s, 'synthetic parity fixture')", [
                (_ALIASES["conflict"], owner, "confirmed"),
                (_ALIASES["conflict"], bot_a, "candidate"),
                (_ALIASES["candidate"], owner, "candidate"),
                (_ALIASES["undone"], owner, "undone"),
            ],
        )
        cur.executemany(
            "INSERT INTO memory.messages (uid, source, agent_id, scope, role, content, author, conversation_id,"
            " session_id, ts, speaker, speaker_ref, forgotten_at, content_sha256)"
            " VALUES (%s, %s, %s, %s, 'user', %s, %s, %s, %s, %s, %s, %s, %s, %s)", [
                (
                    uid, "messages" if agent is None else "agent", agent,
                    "agents" if agent is None else "agent", row.content,
                    row.author,
                    f"messages:{row.platform}:parity-room" if agent is None else None,
                    None if agent is None else f"{agent}-session",
                    row.at, "outside" if role is None else _SPEAKERS[role],
                    _ALIASES["owner_gateway"] if role == "owner" else row.author, row.forgotten_at,
                    sha256(row.content.encode("utf-8")).hexdigest(),
                )
                for uid, agent, role, row in rows
            ],
        )


@pytest.fixture
def parity_store(
    _memory_store: memory_store.ProcessStore,
    monkeypatch: pytest.MonkeyPatch,
    record_property: Callable[[str, str], None],
) -> Iterator[ParityStore]:
    """Use this fixture's Docker DSN even when the global test store is configured."""
    import hermes_state_store

    cargo = memory_store._which("cargo")
    if cargo is None:
        pytest.skip("store reader parity requires Cargo on PATH")
    docker = memory_store._which("docker")
    if docker is None:
        pytest.skip("store reader parity requires Docker on PATH")
    cluster = memory_store._start_docker()
    if cluster is None:
        pytest.skip("store reader parity requires a reachable Docker daemon")

    named = re.fullmatch(r"docker \((lina-test-pg-[0-9a-f]{12})\)", cluster.how)
    try:
        assert named is not None, "Docker store helper must report its exact container name"
        name = named[1]
        memory_store.bootstrap(cluster.dsn)
        _seed(memory_store._with_db(cluster.dsn, memory_store.TEMPLATE_DB))
        dsn = memory_store._as_app(cluster.dsn, memory_store.TEMPLATE_DB)
        monkeypatch.setenv("LINA_STORE_DSN", dsn)
        monkeypatch.setattr(hermes_state_store, "resolve_dsn", lambda: dsn)
        monkeypatch.setattr(hermes_state_store, "resolve_owner_dsn", lambda: dsn)
        yield ParityStore(dsn, cargo, _AGENTS, _PEOPLE, _ALIASES, _DOCUMENTS, _PRIVATE_UIDS)
    finally:
        cluster.stop()
        if named is not None:
            remaining = subprocess.run(
                [docker, "ps", "--all", "--filter", f"name=^/{name}$", "--format", "{{.Names}}"],
                capture_output=True, text=True, timeout=60, check=True,
            )
            assert not remaining.stdout.strip(), f"Disposable Docker container {name} still exists"
            record_property("removed_container", name)
