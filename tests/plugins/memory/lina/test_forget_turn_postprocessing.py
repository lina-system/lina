"""Observe model traffic and external-memory submissions across real turn finalization."""

from contextlib import ExitStack
from unittest.mock import patch

import pytest

from agent import prepared_compaction
from agent.turn_context import OWNER_TASTE_FORGET_EXIT_REASON, PRE_LLM_BLOCK_EXIT_REASON
from hermes_cli.plugins import PluginContext, PluginManifest, get_plugin_manager
from tests.plugins.memory.lina import test_cache_order, test_turn_taste_routes as routes
from tests.plugins.memory.lina.test_taste_cache_order import seed_taste

home = test_cache_order.home


@pytest.mark.parametrize("turn", ["ordinary", "forget", "veto"])
def test_turn_end_model_and_memory_traffic(home, memory_store_sql, monkeypatch, record_property, turn):
    original_configure = routes._configure

    def configure(home, url, memory):
        original_configure(home, url, memory)
        path = home / "config.yaml"
        config = path.read_text().replace("engine: lcm-x", "engine: compressor")
        config = config.replace(
            "auxiliary:\n",
            f"auxiliary:\n  compression: {{provider: custom, base_url: '{url}', api_key: loopback, model: summary-probe}}\n",
        )
        path.write_text(config + "compression:\n  micro_compact: true\n  protect_first_n: 1\n  protect_last_n: 2\n")

    monkeypatch.setattr(routes, "_configure", configure)
    seed_taste(memory_store_sql, "워터컬러", "active")
    with routes._route_agent(home, "cli") as (agent, requests):
        assert agent.context_compressor._micro_compact_enabled is True
        history = []
        for index in range(12):
            history.extend([{"role": "user", "content": f"garden question {index}"},
                            {"role": "assistant", "content": f"answer {index}: " + "fresh soil " * 100}])
        command = "Explain how to plant the next garden bed." if turn == "ordinary" else "워터컬러 취향 잊어줘"
        plugin = PluginContext(PluginManifest(name="test-turn-end", source="user"), get_plugin_manager())
        ended = []
        with ExitStack() as stack:
            stack.callback(plugin.register_hook("on_session_end", lambda turn_exit_reason: ended.append(turn_exit_reason)).dispose)
            if turn == "veto":
                stack.callback(plugin.register_hook("pre_llm_call", lambda: {"action": "block", "message": "Blocked."}).dispose)
            memory = agent._memory_manager
            queue = stack.enter_context(patch.object(memory, "queue_prefetch_all", wraps=memory.queue_prefetch_all))
            sync = stack.enter_context(patch.object(memory, "sync_all", wraps=memory.sync_all))
            prepare = stack.enter_context(patch.object(prepared_compaction, "maybe_prepare", wraps=prepared_compaction.maybe_prepare))
            result = agent.run_conversation(command, conversation_history=history)

        models = [req.body["model"] for req in requests]
        assert result["completed"] and ended == [result["turn_exit_reason"]]
        if turn != "ordinary":
            assert result["turn_exit_reason"] == (PRE_LLM_BLOCK_EXIT_REASON if turn == "veto" else OWNER_TASTE_FORGET_EXIT_REASON)
        counts = (result["api_calls"], models.count("summary-probe"), queue.call_count, sync.call_count)
        record_property("primary_summary_prefetch_sync", str(counts))
        assert counts == ((1, 1, 1, 1) if turn == "ordinary" else (0, 0, 0, 0))
        assert len(models) == counts[0] + counts[1]
        assert sum(call.kwargs["origin"] == "turn-end" for call in prepare.call_args_list) == (turn == "ordinary")
