"""Exercise owner-forget failures and policy vetoes through the real agent turn."""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Final, TypedDict
from unittest.mock import patch

import pytest
from psycopg.errors import RaiseException

from agent.message_metadata import PRE_LLM_BLOCK_WITHHELD_PROMPT, withheld_prompt_text
from agent.turn_context import PRE_LLM_BLOCK_EXIT_REASON
from hermes_cli.heart import forget
from hermes_cli.heart.locale import text
from hermes_cli.plugins import PluginContext, PluginManifest, get_plugin_manager
from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_taste_cache_order import StoreSQL, seed_taste
from tests.plugins.memory.lina.test_turn_taste_routes import _route_agent

home = test_cache_order.home

LABEL: Final = "synthetic canvas glaze"
COMMAND: Final = f"Forget the preference for {LABEL}"
FAILURE: Final = "synthetic taste storage failure"
BLOCK_MESSAGE: Final = "Blocked by the synthetic policy."


def test_owner_forget_propagates_storage_failure_without_leaking_label(
    home: Path, memory_store_sql: StoreSQL, caplog: pytest.LogCaptureFixture,
) -> None:
    seed_taste(memory_store_sql, LABEL, "active")
    memory_store_sql(
        """
        CREATE FUNCTION memory.reject_test_forget() RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
            RAISE EXCEPTION 'synthetic taste storage failure';
        END;
        $$
        """
    )
    memory_store_sql(
        """
        CREATE TRIGGER reject_test_forget BEFORE UPDATE OF status ON memory.tastes
        FOR EACH ROW WHEN (NEW.status = 'forgotten')
        EXECUTE FUNCTION memory.reject_test_forget()
        """
    )
    try:
        with caplog.at_level(logging.INFO), _route_agent(home, "cli") as (agent, requests):
            with pytest.raises(RaiseException) as raised:
                agent.run_conversation(COMMAND)
            assert raised.value.diag.message_primary == FAILURE
            assert requests == []
            history = agent._session_db.get_messages_as_conversation(agent.session_id)
            assert all(row["content"] != text("forget_done") for row in history if row["role"] == "assistant")
    finally:
        memory_store_sql("DROP TRIGGER reject_test_forget ON memory.tastes")
        memory_store_sql("DROP FUNCTION memory.reject_test_forget()")

    assert memory_store_sql("SELECT status FROM memory.tastes WHERE label = %s", (LABEL,)) == [("active",)]
    formatter = logging.Formatter()
    leaks = [
        (record.name, record.filename, record.lineno, record.levelname)
        for record in caplog.records
        if LABEL.casefold() in formatter.format(record).casefold()
    ]
    assert leaks == []


class Veto(TypedDict):
    action: str
    message: str


def test_pre_llm_veto_precedes_owner_forget_and_persists_withheld_prompt(
    home: Path, memory_store_sql: StoreSQL,
) -> None:
    seed_taste(memory_store_sql, LABEL, "active")

    def veto() -> Veto:
        return {"action": "block", "message": BLOCK_MESSAGE}

    with _route_agent(home, "cli") as (agent, requests):
        first = agent.run_conversation("Ready for the next instruction.")
        assert first["completed"] and agent._owner_taste_allowed
        assert len(requests) == 1
        plugin = PluginContext(PluginManifest(name="test-policy", source="user"), get_plugin_manager())
        registration = plugin.register_hook("pre_llm_call", veto)
        try:
            with patch.object(forget, "forget_from_conversation", wraps=forget.forget_from_conversation) as attempt:
                result = agent.run_conversation(COMMAND, conversation_history=first["messages"])
            attempt.assert_not_called()
        finally:
            registration.dispose()

        assert len(requests) == 1
        assert result["turn_exit_reason"] == PRE_LLM_BLOCK_EXIT_REASON
        assert result["final_response"] == BLOCK_MESSAGE
        user, assistant = agent._session_db.get_messages_as_conversation(agent.session_id)[-2:]
        assert (user["role"], user["content"]) == ("user", PRE_LLM_BLOCK_WITHHELD_PROMPT)
        assert withheld_prompt_text(user) == COMMAND
        assert (assistant["role"], assistant["content"]) == ("assistant", BLOCK_MESSAGE)

    assert memory_store_sql("SELECT status FROM memory.tastes WHERE label = %s", (LABEL,)) == [("active",)]
