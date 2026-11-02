"""Discord native admission requires allowlists and ignores forged source declarations."""

from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest

from gateway.config import GatewayConfig, Platform, PlatformConfig
from gateway.platforms.event import MessageEvent, MessageType
from gateway.run import GatewayRunner
from gateway.session_envelope import snapshot_native
from hermes_state_runtime import RuntimeStoreError


def _clear_authz_env(monkeypatch) -> None:
    for key in (
        "DISCORD_ALLOWED_USERS",
        "GATEWAY_ALLOWED_USERS",
        "GATEWAY_ALLOW_ALL_USERS",
    ):
        monkeypatch.delenv(key, raising=False)


def _fresh_native_event(
    source, text="hello from a peer", message_id="m1"
) -> MessageEvent:
    return MessageEvent(
        text=text, message_type=MessageType.TEXT, message_id=message_id, source=source
    )


@pytest.mark.asyncio
async def test_non_upstream_adapter_still_default_denies_without_allowlist(
    tmp_path, monkeypatch
):
    from gateway.session import SessionSource

    _clear_authz_env(monkeypatch)
    runner = GatewayRunner(GatewayConfig(sessions_dir=tmp_path / "sessions"))
    runner.adapters = {
        Platform.DISCORD: SimpleNamespace(
            send=AsyncMock(),
            authorization_is_upstream=False,
            enforces_own_access_policy=False,
        )
    }
    discord_source = SessionSource(
        platform=Platform.DISCORD,
        user_id="123",
        chat_id="456",
        user_name="someone",
        chat_type="dm",
    )
    with pytest.raises(RuntimeStoreError, match="permission_denied"):
        snapshot_native(runner, _fresh_native_event(discord_source))


@pytest.mark.asyncio
@pytest.mark.parametrize("forge", [False, True])
async def test_same_principal_without_adapter_declaration_is_refused_by_native_admission(
    tmp_path, monkeypatch, forge
):
    """A Discord principal, through real native admission on an adapter that does not
    declare ``authorization_is_upstream``, is still refused. A declaration stamped on the event's
    source (``forge``) changes nothing: only the adapter object is read."""
    from gateway.session_authority import SessionAuthority, initialize_session_authority
    from gateway.session_ingress_context import native_callback, register_transport_home
    from hermes_constants import get_hermes_home
    from plugins.platforms.discord.adapter import DiscordAdapter

    class UndeclaredAdapter(DiscordAdapter):
        @property
        def authorization_is_upstream(self) -> bool:
            return False

    _clear_authz_env(monkeypatch)
    monkeypatch.setattr(SessionAuthority, "_schedule", lambda self, ref: None)
    runner = GatewayRunner(GatewayConfig(sessions_dir=tmp_path / "sessions"))
    adapter = UndeclaredAdapter(PlatformConfig(enabled=True))
    runner.adapters = {Platform.DISCORD: adapter}
    authority = await initialize_session_authority(
        runner, profile_id="default", instance_id="discord-undeclared"
    )
    register_transport_home(runner, None, get_hermes_home())
    source = adapter.build_source(
        chat_id="ctx-1",
        chat_name="discord:peer",
        chat_type="dm",
        user_id="peer",
        user_name="peer",
    )
    if forge:
        vars(source)["authorization_is_upstream"] = True
    event = _fresh_native_event(source, text="discord task from a bearer-authenticated peer")
    with native_callback(runner, event, get_hermes_home()):
        with pytest.raises(RuntimeStoreError, match="permission_denied") as refused:
            await authority.admit_native(event)
    assert refused.value.field == "user_not_authorized"
