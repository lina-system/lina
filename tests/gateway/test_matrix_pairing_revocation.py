"""Matrix live pairing revocation."""
import os
from types import SimpleNamespace
import pytest
from gateway.session import Platform, SessionSource
from tests.gateway import test_pairing_allowlist_bypass as shared

store = shared.store
_isolate_env = shared._isolate_env

@pytest.mark.asyncio
async def test_matrix_pairing_revoke_denies_live_message_and_approval_without_restart(store):
    """The runner's live pairing verdict must gate both Matrix messages and approvals."""
    from unittest.mock import AsyncMock, patch

    from gateway.config import GatewayConfig, PlatformConfig
    from gateway.run import GatewayRunner
    from plugins.platforms.matrix.adapter import MatrixAdapter, _MatrixApprovalPrompt

    user_id = "@paired:example.org"
    room_id = "!room:example.org"
    config = PlatformConfig(
        enabled=True,
        extra={"homeserver": "https://matrix.example.org", "user_id": "@bot:example.org"},
    )
    adapter = MatrixAdapter(config)
    runner = object.__new__(GatewayRunner)
    runner.config = GatewayConfig(platforms={Platform.MATRIX: config})
    runner.adapters = {Platform.MATRIX: adapter}
    runner._profile_adapters = {}
    runner.pairing_store = store
    runner.pairing_stores = {}
    adapter.set_authorization_check(runner._make_adapter_auth_check(Platform.MATRIX))

    source = SessionSource(
        platform=Platform.MATRIX,
        user_id=user_id,
        chat_id=room_id,
        user_name="Paired User",
        chat_type="dm",
    )
    assert runner._is_user_authorized(source) is False

    code = store.generate_code("matrix", user_id, "Paired User")
    assert code is not None
    assert store.approve_code("matrix", code) is not None
    assert runner._is_user_authorized(source) is True

    adapter._send_invalid_reaction_feedback = AsyncMock()

    async def react(prompt_id: str, reaction_id: str):
        prompt = _MatrixApprovalPrompt(
            session_key=f"session-{prompt_id}",
            chat_id=room_id,
            message_id=prompt_id,
            requester_user_id=user_id,
        )
        adapter._approval_prompts_by_event[prompt_id] = prompt
        event = SimpleNamespace(
            sender=user_id,
            event_id=reaction_id,
            room_id=room_id,
            content={"m.relates_to": {"event_id": prompt_id, "key": "✅"}},
        )
        with patch("tools.approval.resolve_gateway_approval", return_value=1) as resolve:
            await adapter._on_reaction(event)
        return prompt, resolve

    before_prompt, before_resolve = await react("$before", "$reaction-before")
    assert before_prompt.resolved is True
    before_resolve.assert_called_once_with("session-$before", "once")

    assert store.revoke("matrix", user_id) is True
    assert store.is_approved("matrix", user_id) is False
    assert runner._is_user_authorized(source) is False

    after_prompt, after_resolve = await react("$after", "$reaction-after")
    assert after_prompt.resolved is False
    after_resolve.assert_not_called()


def test_matrix_pairing_revoke_purges_live_allowlist_snapshot(store, monkeypatch):
    """A live Matrix adapter must not retain an allowlist entry removed by pairing revoke."""
    from gateway.config import PlatformConfig
    import gateway.run as gateway_run
    import hermes_cli.config as cfg
    from plugins.platforms.matrix.adapter import MatrixAdapter

    owner_id = "@owner:example.org"
    paired_id = "@paired:example.org"
    monkeypatch.setenv("MATRIX_ALLOWED_USERS", owner_id)
    monkeypatch.setattr(
        cfg,
        "save_env_value",
        lambda key, value: os.environ.__setitem__(key, value),
    )
    monkeypatch.setattr(
        cfg,
        "remove_env_value",
        lambda key: (os.environ.pop(key, None), True)[1],
    )

    code = store.generate_code("matrix", paired_id, "Paired User")
    assert code is not None
    assert store.approve_code("matrix", code) is not None
    assert os.environ["MATRIX_ALLOWED_USERS"] == f"{owner_id},{paired_id}"

    adapter = MatrixAdapter(
        PlatformConfig(
            enabled=True,
            extra={"homeserver": "https://matrix.example.org", "user_id": "@bot:example.org"},
        )
    )
    assert adapter._is_authorized_user(paired_id) is True
    held = adapter._allowed_user_ids  # e.g. a Discord approval view's reference

    runner = SimpleNamespace(
        adapters={Platform.MATRIX: adapter},
        _profile_adapters={},
    )
    monkeypatch.setattr(gateway_run, "_gateway_runner_ref", lambda: runner)

    assert store.revoke("matrix", paired_id) is True
    assert os.environ["MATRIX_ALLOWED_USERS"] == owner_id
    assert paired_id not in adapter._allowed_user_ids
    assert paired_id not in held
    assert adapter._is_authorized_user(paired_id) is False
