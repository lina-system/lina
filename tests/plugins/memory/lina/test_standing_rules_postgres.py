"""Regression for #774: render applicable standing rules from real PostgreSQL."""

from __future__ import annotations

import re
from collections.abc import Callable
from contextlib import closing
from pathlib import Path
from types import SimpleNamespace
from typing import Final
from uuid import uuid4

import psycopg
import pytest
from psycopg.types.json import Jsonb

from agent.system_prompt import _frozen_plugin_prompt_sections
from hermes_cli.goals import GoalState, _meta_key
from hermes_state import SessionDB
from plugins.memory.lina.identity import IdentityRegistry
from tests._fixtures.memory_store import ProcessStore

PROFILE: Final = "orchard"
SESSION: Final = "20260102_090000_abcdef"
GATEWAY_KEY: Final = "agent:main:discord:channel:10001"
SHARED: Final = "Use metric units."
AGENT: Final = "Label every seed tray."
CHANNEL: Final = "List the beds by name."
SOURCE: Final = "Include the plot number."
GOAL: Final = "Report how many trees sprouted."
FILTER: Final = "Omit mock supplier circulars."
FILTER_MATCH: Final = "fixture-circulars"
EXCLUDED: Final = (
    "Report how many trees were pruned.",
    "Label the west annex beds.",
    "Follow the south greenhouse checklist.",
    "Count the seeds held for another goal.",
    "Omit mock tool catalogues.",
)


@pytest.fixture
def standing_rules(
    _memory_store: ProcessStore,
    memory_store_sql: Callable[[str, tuple[str, ...]], list[tuple[str, ...]]],
) -> None:
    with closing(IdentityRegistry()) as registry:
        channel = registry.resolve(
            "conversation",
            [("gateway_chat", f"{PROFILE}:{GATEWAY_KEY}"), ("messages_room", "discord:10001")],
        )
        other_channel = registry.resolve(
            "conversation",
            [("gateway_chat", f"{PROFILE}:agent:main:discord:channel:10002"),
             ("messages_room", "discord:10002")],
        )
    assert channel and other_channel and channel != other_channel

    # The shared rule is newest. Sorting by date alone would put the place rules first.
    rows = (
        (CHANNEL, "place", "*", f"channel:{channel}", "2026-01-01", None),
        (SOURCE, "place", "*", "source:messages_room:discord:10001", "2026-01-02", None),
        (GOAL, "place", f"agent:{PROFILE}", f"goal:{SESSION}", "2026-01-03", None),
        (AGENT, "agent", f"agent:{PROFILE}", None, "2026-01-04", None),
        (SHARED, "all", "*", None, "2026-01-05", None),
        (FILTER, "place", "*", f"channel:{channel}", "2026-01-05", FILTER_MATCH),
        (EXCLUDED[0], "place", "agent:greenhouse", f"channel:{channel}", "2026-01-05", None),
        (EXCLUDED[1], "place", "*", f"channel:{other_channel}", "2026-01-05", None),
        (EXCLUDED[2], "place", "*", "source:messages_room:discord:10002", "2026-01-05", None),
        (EXCLUDED[3], "place", "*", "goal:another-session", "2026-01-05", None),
        (EXCLUDED[4], "place", "*", f"channel:{other_channel}", "2026-01-05", "fixture-catalogues"),
    )
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as app:
        app.execute("SELECT set_config('lina.agent_id', %s, false)", (PROFILE,))
        for index, (text, scope, target, place, date, match) in enumerate(rows):
            uid = f"agent:{PROFILE}:owner-fixture-{index}"
            memory_store_sql(
                """INSERT INTO memory.messages
                   (uid, source, agent_id, session_id, role, content, content_sha256, speaker, ts)
                   VALUES (%s, 'agent', %s, 'authoring-session', 'user', %s, 'fixture', 'owner', %s)""",
                (uid, PROFILE, text, date),
            )
            app.execute(
                """SELECT memory.record_standing_rule(
                       %s, %s, %s, %s, %s, %s, %s, p_filter_match => %s)""",
                (uuid4(), uid, "conduct" if match is None else "filter", scope, target, place, text,
                 Jsonb({"kind": match}) if match is not None else None),
            )


@pytest.mark.usefixtures("standing_rules")
@pytest.mark.parametrize("goal_status", ["active", "done"])
def test_frozen_rules_render_in_scope_order_without_filter_bodies(tmp_path: Path, goal_status: str) -> None:
    home = tmp_path / "profiles" / PROFILE
    home.mkdir(parents=True)
    with SessionDB(home / "state.db") as db:
        db.ensure_session(SESSION, source="discord")
        db.set_meta(_meta_key(SESSION), GoalState(goal="Start a test orchard", status=goal_status).to_json())
        agent = SimpleNamespace(session_id=SESSION, _session_db=db, _gateway_session_key=GATEWAY_KEY)
        sections = _frozen_plugin_prompt_sections(agent)

    rules = [section.content for section in sections if section.id == "owner-standing-rules"]
    assert len(rules) == 1, "Applicable PostgreSQL rules must produce one frozen section"
    (content,) = rules
    dated_rules = [(SHARED, "2026-01-05"), (AGENT, "2026-01-04"),
                   (CHANNEL, "2026-01-01"), (SOURCE, "2026-01-02"), (GOAL, "2026-01-03")]
    expected = dated_rules if goal_status == "active" else dated_rules[:-1]
    rendered = []
    for line in content.splitlines():
        for text, date in dated_rules:
            if text in line:
                assert date in line
                rendered.append((text, date))
    assert rendered == expected
    assert all(text not in content for text in (*EXCLUDED, FILTER, FILTER_MATCH))

    # The summary reports the number of applicable filters, without exposing their contents.
    assert re.search(r"(?im)^.*\bfilters?\b.*\b1\b|^.*\b1\b.*\bfilters?\b", content)
