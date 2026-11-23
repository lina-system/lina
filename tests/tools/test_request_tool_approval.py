"""Tests for tools.approval.request_tool_approval — the plugin pre_tool_call
``{"action": "approve"}`` escalation into the human-approval gate.

These verify that a plugin-driven approval reuses the SAME machinery as a
Tier-2 dangerous-command match: session/permanent allowlist, the CLI prompt,
the gateway submit_pending path, cron_mode, and fail-closed timeouts.
"""

import hashlib
import json
from concurrent.futures import ThreadPoolExecutor
from queue import Queue

import pytest

from tools import approval
from tools import approval_prompt
import tools.approval_context as tools_approval_context
from tools import approval_context
from tools.approval import request_tool_approval


@pytest.fixture(autouse=True)
def _isolate_approval_state(monkeypatch):
    """Give each test a clean session key and empty allowlists."""
    monkeypatch.setattr(
        approval, "get_current_session_key",
        lambda default="default": "test-session",
    )
    monkeypatch.setattr(
        tools_approval_context, "get_current_session_key",
        lambda default="default": "test-session",
    )
    # Empty session + permanent approval stores so nothing pre-approves.
    monkeypatch.setattr(approval, "is_approved", lambda sk, pk: False)
    # Not a yolo session (the shared gate checks this first).
    monkeypatch.setattr(approval, "is_current_session_yolo_enabled", lambda: False)
    monkeypatch.setattr(approval, "_YOLO_MODE_FROZEN", False, raising=False)
    # No thread-registered CLI callback by default.
    monkeypatch.setattr(
        "tools.terminal_tool._get_approval_callback", lambda: None, raising=False
    )
    yield


@pytest.fixture
def sample_tool_dispatch(monkeypatch):
    import model_tools
    from tools.registry import ToolRegistry

    executed = []

    def handler(args):
        executed.append(args)
        return json.dumps({"delivered": args["body"]})

    registry = ToolRegistry()
    registry.register(name="send_sample", toolset="test", schema={}, handler=handler)
    monkeypatch.setattr(model_tools, "registry", registry)
    return model_tools.handle_function_call, executed


class TestRequestToolApproval:
    def test_session_cached_approval_short_circuits(self, monkeypatch):
        monkeypatch.setattr(approval, "is_approved", lambda sk, pk: True)
        # Should NOT prompt at all.
        monkeypatch.setattr(
            approval, "prompt_dangerous_approval",
            lambda *a, **k: pytest.fail("should not prompt when already approved"),
        )
        monkeypatch.setattr(
            approval_prompt, "prompt_dangerous_approval",
            lambda *a, **k: pytest.fail("should not prompt when already approved"),
        )
        res = request_tool_approval("write_file", "sensitive path", rule_key="ssh")
        assert res == {"approved": True, "message": None}

    def test_cli_approve_once(self, monkeypatch):
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: True)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "prompt_dangerous_approval", lambda *a, **k: "once")
        monkeypatch.setattr(approval_prompt, "prompt_dangerous_approval", lambda *a, **k: "once")
        res = request_tool_approval("write_file", "writing ~/.ssh/authorized_keys")
        assert res["approved"] is True

    def test_cli_deny_blocks(self, monkeypatch):
        from hermes_cli import lifecycle

        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: True)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "prompt_dangerous_approval", lambda *a, **k: "deny")
        monkeypatch.setattr(approval_prompt, "prompt_dangerous_approval", lambda *a, **k: "deny")
        events = []
        monkeypatch.setattr(
            lifecycle,
            "invoke_hook",
            lambda hook_name, **kwargs: events.append((hook_name, kwargs)) or [],
        )
        tokens = approval_context.set_current_observability_context(
            turn_id="turn-1",
            tool_call_id="call-1",
        )
        try:
            res = request_tool_approval("terminal", "curl PUT to external API")
        finally:
            approval_context.reset_current_observability_context(tokens)
        assert res["approved"] is False
        assert "denied" in res["message"].lower()
        assert res["pattern_key"].startswith("plugin_rule:")
        assert [name for name, _ in events] == [
            "pre_approval_request",
            "post_approval_response",
        ]
        assert all(event["turn_id"] == "turn-1" for _, event in events)
        assert all(event["tool_call_id"] == "call-1" for _, event in events)
        assert events[-1][1]["choice"] == "deny"

    def test_cli_session_persists_session_only(self, monkeypatch):
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: True)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "prompt_dangerous_approval", lambda *a, **k: "session")
        monkeypatch.setattr(approval_prompt, "prompt_dangerous_approval", lambda *a, **k: "session")
        calls = {"session": [], "permanent": []}
        monkeypatch.setattr(approval, "approve_session",
                            lambda sk, pk: calls["session"].append(pk))
        monkeypatch.setattr(approval, "approve_permanent",
                            lambda pk: calls["permanent"].append(pk))
        monkeypatch.setattr(approval, "save_permanent_allowlist", lambda x: None)
        res = request_tool_approval("write_file", "reason", rule_key="ssh-writes")
        assert res["approved"] is True
        assert calls["session"] == ["plugin_rule:ssh-writes"]
        assert calls["permanent"] == []  # session != always


    def test_cron_deny_mode_blocks(self, monkeypatch):
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: False)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_cron_approval_context", lambda: True)
        monkeypatch.setattr(tools_approval_context, "_is_cron_approval_context", lambda: True)
        monkeypatch.setattr(approval_context, "_get_cron_approval_mode", lambda: "deny")
        res = request_tool_approval("terminal", "smtp send")
        assert res["approved"] is False
        assert "cron" in res["message"].lower()

    def test_cron_approve_mode_allows(self, monkeypatch):
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: False)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_cron_approval_context", lambda: True)
        monkeypatch.setattr(tools_approval_context, "_is_cron_approval_context", lambda: True)
        monkeypatch.setattr(approval_context, "_get_cron_approval_mode", lambda: "approve")
        res = request_tool_approval("terminal", "smtp send")
        assert res["approved"] is True


    def test_distinct_reasons_get_distinct_keys(self, monkeypatch):
        """Two different reasons on the SAME tool must not share an [a]lways
        allowlist entry (Finding 3: tool_name alone was too coarse)."""
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: True)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "prompt_dangerous_approval", lambda *a, **k: "deny")
        monkeypatch.setattr(approval_prompt, "prompt_dangerous_approval", lambda *a, **k: "deny")
        k1 = request_tool_approval("write_file", "write to ~/.ssh")["pattern_key"]
        k2 = request_tool_approval("write_file", "send an email")["pattern_key"]
        assert k1 != k2

    def test_explicit_rule_key_overrides_derivation(self, monkeypatch):
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: True)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "prompt_dangerous_approval", lambda *a, **k: "deny")
        monkeypatch.setattr(approval_prompt, "prompt_dangerous_approval", lambda *a, **k: "deny")
        res = request_tool_approval("terminal", "any", rule_key="my-rule")
        assert res["pattern_key"] == "plugin_rule:my-rule"

    def test_no_human_non_cron_fails_closed(self, monkeypatch):
        """Non-interactive, non-gateway, NON-cron context blocks (fail-closed)
        — a plugin-flagged action never runs ungated without a human."""
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: False)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_cron_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_cron_approval_context", lambda: False)
        res = request_tool_approval("terminal", "smtp send")
        assert res["approved"] is False
        assert "no interactive user or gateway" in res["message"].lower()

    def test_api_server_exec_ask_uses_approval_bridge(self, monkeypatch):
        """Plugin escalation reaches the /v1/runs approval bridge when ask mode is active."""
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: False)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_cron_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_cron_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_single_query_approval_context", lambda: False)
        monkeypatch.setattr(approval_context, "_get_approval_mode", lambda: "manual")
        monkeypatch.setenv("HERMES_EXEC_ASK", "1")
        monkeypatch.setenv("HERMES_SESSION_PLATFORM", "api_server")

        notified = []

        def approve(data):
            notified.append(data)
            assert approval.resolve_gateway_approval(
                "test-session", "once", request_id=data["request_id"]
            ) == 1

        approval.register_gateway_notify("test-session", approve)
        try:
            res = request_tool_approval("home_lock", "unlock the front door", rule_key="unlock")
        finally:
            approval.unregister_gateway_notify("test-session")

        assert res["approved"] is True
        assert len(notified) == 1
        assert notified[0]["pattern_key"] == "plugin_rule:unlock"

    def test_api_server_without_exec_ask_remains_fail_closed(self, monkeypatch):
        """An api_server call without an active approval bridge must not run ungated."""
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: False)
        monkeypatch.setattr(approval, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_gateway_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_cron_approval_context", lambda: False)
        monkeypatch.setattr(tools_approval_context, "_is_cron_approval_context", lambda: False)
        monkeypatch.setattr(approval, "_is_single_query_approval_context", lambda: False)
        monkeypatch.setattr(approval_context, "_get_approval_mode", lambda: "manual")
        monkeypatch.setattr(approval_context, "_get_unattended_approval_mode", lambda: "deny")
        monkeypatch.delenv("HERMES_EXEC_ASK", raising=False)
        monkeypatch.setenv("HERMES_SESSION_PLATFORM", "api_server")

        res = request_tool_approval("home_lock", "unlock the front door", rule_key="unlock")

        assert res["approved"] is False
        assert "unattended platform" in res["message"].lower()

    def test_plugin_queue_binds_modified_tool_args_without_disclosing_them(self, monkeypatch):
        from hermes_cli import lifecycle, plugins

        monkeypatch.setattr(approval, "_presence", lambda callback: (callback, False, True, False))
        monkeypatch.setattr(approval_context, "_get_approval_mode", lambda: "manual")
        monkeypatch.setattr(approval_context, "_fire_approval_hook", lambda *args, **kwargs: None)
        monkeypatch.setattr(lifecycle, "invoke_hook", lambda *args, **kwargs: [
            {"action": "modify", "args": {"body": "synthetic-private-body"}},
            {"action": "approve", "message": "Confirm delivery"},
        ])
        encoded = b'{"args":{"body":"synthetic-private-body","z":2},"name":"send_sample"}'
        version = hashlib.sha256(encoded).hexdigest()
        observed = {}

        def notify(data):
            snapshot = approval.list_all_gateway_approvals()["test-session"][0]
            snapshot["pattern_keys"].clear()
            snapshot["content_version"] = "modified snapshot"
            observed["row"] = approval.list_all_gateway_approvals()["test-session"][0]
            observed["count"] = approval.resolve_gateway_approval(
                "test-session", "once", request_id=data["request_id"], content_version=version,
            )
            if not observed["count"]:
                approval.resolve_gateway_approval("test-session", "deny", request_id=data["request_id"])

        approval.register_gateway_notify("test-session", notify)
        try:
            block, modified = plugins._dispatch_pre_tool_call_hooks("send_sample", {"z": 2, "body": "old body"})
        finally:
            approval.unregister_gateway_notify("test-session")

        assert block is None
        assert modified == {"z": 2, "body": "synthetic-private-body"}
        assert observed["count"] == 1
        row = observed["row"]
        assert row["content_version"] == version
        assert row["pattern_keys"] == ["plugin_rule:send_sample"]
        assert "synthetic-private-body" not in repr(row)

    def test_distinct_args_of_same_plugin_tool_can_wait_for_separate_decisions(
        self, monkeypatch, sample_tool_dispatch,
    ):
        from hermes_cli import lifecycle

        dispatch, executed = sample_tool_dispatch
        monkeypatch.setattr(approval, "_presence", lambda callback: (callback, False, True, False))
        monkeypatch.setattr(approval_context, "_get_approval_config", lambda: {"mode": "manual", "timeout": 30})
        monkeypatch.setattr(lifecycle, "invoke_hook", lambda hook_name, **kwargs: (
            [{"action": "approve", "message": "Confirm delivery"}] if hook_name == "pre_tool_call" else []
        ))
        notices = Queue()
        versions = {
            body: hashlib.sha256(f'{{"args":{{"body":"{body}"}},"name":"send_sample"}}'.encode()).hexdigest()
            for body in ("first sample", "second sample")
        }

        with ThreadPoolExecutor(max_workers=2) as workers:
            approval.register_gateway_notify("test-session", notices.put)
            try:
                first = workers.submit(dispatch, "send_sample", {"body": "first sample"})
                first_notice = notices.get(timeout=5)
                second = workers.submit(dispatch, "send_sample", {"body": "second sample"})
                second_notice = notices.get(timeout=5)
                pending = approval.list_all_gateway_approvals()["test-session"]
                assert {row["request_id"]: row["content_version"] for row in pending} == {
                    first_notice["request_id"]: versions["first sample"],
                    second_notice["request_id"]: versions["second sample"],
                }
                assert len(pending) == 2
                assert executed == []

                assert approval.resolve_gateway_approval(
                    "test-session", "once", request_id=first_notice["request_id"],
                    content_version=versions["first sample"],
                ) == 1
                assert json.loads(first.result(timeout=5)) == {"delivered": "first sample"}
                assert not second.done()
                assert approval.resolve_gateway_approval(
                    "test-session", "deny", request_id=second_notice["request_id"],
                    content_version=versions["second sample"],
                ) == 1
                assert "error" in json.loads(second.result(timeout=5))
            finally:
                approval.unregister_gateway_notify("test-session")

        assert executed == [{"body": "first sample"}]

    def test_unguarded_plugin_dispatch_keeps_handler_result_without_calling_gate(
        self, monkeypatch, sample_tool_dispatch,
    ):
        from hermes_cli import lifecycle

        dispatch, executed = sample_tool_dispatch
        hook_calls = []

        def no_approval(hook_name, **kwargs):
            if hook_name == "pre_tool_call":
                hook_calls.append(kwargs["tool_name"])
            return []

        def unexpected_gate(*args, **kwargs):
            pytest.fail("an unguarded tool must not call the approval gate")

        monkeypatch.setattr(lifecycle, "invoke_hook", no_approval)
        monkeypatch.setattr(approval, "request_tool_approval", unexpected_gate)
        args = {"body": "ordinary sample"}
        baseline = dispatch("send_sample", args, skip_pre_tool_call_hook=True)

        assert dispatch("send_sample", args) == baseline
        assert json.loads(baseline) == {"delivered": "ordinary sample"}
        assert hook_calls == ["send_sample"]
        assert executed == [args, args]

    def test_yolo_session_bypasses_gate(self, monkeypatch):
        """A --yolo session skips the plugin approval gate (parity with the
        dangerous-command path, via the shared _run_approval_gate)."""
        monkeypatch.setattr(approval, "is_current_session_yolo_enabled", lambda: True)
        monkeypatch.setattr(
            approval, "prompt_dangerous_approval",
            lambda *a, **k: pytest.fail("yolo must not prompt"),
        )
        monkeypatch.setattr(
            approval_prompt, "prompt_dangerous_approval",
            lambda *a, **k: pytest.fail("yolo must not prompt"),
        )
        res = request_tool_approval("terminal", "curl PUT", rule_key="ext")
        assert res == {"approved": True, "message": None}

    def test_approvals_mode_off_bypasses_gate(self, monkeypatch):
        """``approvals.mode: off`` (the Desktop "Approvals: off" toggle) must bypass the shared gate
        exactly like the shell guards do — otherwise computer_use / plugin-rule / SSH-config-write
        approvals keep prompting a user who turned approvals off."""
        monkeypatch.setattr(approval_context, "_get_approval_mode", lambda: "off")
        monkeypatch.setattr(approval, "_is_interactive_cli", lambda: True)
        monkeypatch.setattr(
            approval, "prompt_dangerous_approval",
            lambda *a, **k: pytest.fail("approvals.mode=off must not prompt"),
        )
        monkeypatch.setattr(
            approval_prompt, "prompt_dangerous_approval",
            lambda *a, **k: pytest.fail("approvals.mode=off must not prompt"),
        )
        res = request_tool_approval("computer_use", "click", rule_key="cua")
        assert res == {"approved": True, "message": None}
