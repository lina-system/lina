"""Matrix scoped YAML settings and authorization."""
import asyncio
import types
from unittest.mock import AsyncMock, patch
from gateway.config import Platform, load_gateway_config
from tests.gateway import test_adapter_settings_scoped_precedence as shared

homes = shared.homes
_secondary_scope = shared._secondary_scope
_plugins = shared._plugins
_allowed_mentions = shared._allowed_mentions

def test_secondary_reads_own_yaml_and_never_the_launch_env(homes, monkeypatch):
    launch, secondary = homes
    (launch / "config.yaml").write_text(
        "matrix:\n  process_notices: true\n  session_scope: room\n"
        "discord:\n  reactions: false\n  allow_mentions:\n    everyone: true\n"
        "slack:\n  reactions: false\n  ignored_channels: [C_LAUNCH]\n"
        "telegram:\n  reactions: true\n", encoding="utf-8")
    load_gateway_config()  # launch profile bridges its YAML into os.environ (single-profile contract)
    (secondary / "config.yaml").write_text(
        "matrix:\n  enabled: true\n  user_id: '@bot:example.org'\n"
        "  allowed_users: ['@owner:example.org']\n  ignore_user_patterns: ['^@ignored:']\n"
        "discord:\n  enabled: true\nslack:\n  enabled: true\n"
        "telegram:\n  enabled: true\n  proxy_url: http://127.0.0.1:18080\n", encoding="utf-8")
    from plugins.platforms.discord.adapter import DiscordAdapter
    from plugins.platforms.matrix.adapter import MatrixAdapter
    from plugins.platforms.slack.adapter import SlackAdapter
    from plugins.platforms.telegram import adapter as tg
    with _secondary_scope(secondary):
        cfg = load_gateway_config()
        m = MatrixAdapter(cfg.platforms[Platform.MATRIX])
        d = DiscordAdapter(cfg.platforms[Platform.DISCORD])
        s = SlackAdapter(cfg.platforms[Platform.SLACK])
        t = tg.TelegramAdapter(cfg.platforms[Platform.TELEGRAM])
        # Omitted keys resolve to the DEFAULT, not the launch profile's bridged env.
        assert (m._process_notices, m._matrix_session_scope) == (False, "auto")
        assert d._reactions_enabled() and s._reactions_enabled() and s._slack_ignored_channels() == set()
        assert not t._reactions_enabled()
        assert _allowed_mentions(d.config.extra).everyone is False
        # The seeded YAML lists reach the Matrix authz consumers.
        assert m._allowed_user_ids == {"@owner:example.org"} and len(m._ignored_user_patterns) == 1
        # The secondary's YAML proxy reaches request construction without an env bridge.
        monkeypatch.setenv("HERMES_TELEGRAM_DISABLE_FALLBACK_IPS", "true")
        built: list = []
        with patch.object(tg, "HTTPXRequest", lambda **kw: built.append(kw) or types.SimpleNamespace()), \
                patch.object(t, "_instrument_polling_request", side_effect=lambda r: r):
            asyncio.run(t._build_ptb_requests())
        assert [kw.get("proxy") for kw in built] == ["http://127.0.0.1:18080"] * 2


def test_matrix_yaml_lists_gate_intake_and_approval(homes):
    from plugins.platforms.matrix.adapter import MatrixAdapter
    _, secondary = homes
    (secondary / "config.yaml").write_text(
        "matrix:\n  enabled: true\n  user_id: '@bot:example.org'\n"
        "  allowed_users: ['@owner:example.org']\n  ignore_user_patterns: ['^@ignored:']\n", encoding="utf-8")
    with _secondary_scope(secondary):
        a = MatrixAdapter(load_gateway_config().platforms[Platform.MATRIX])
        a._user_id = "@bot:example.org"
        a._is_allowed_matrix_room_event = AsyncMock(return_value=True)
        a._handle_text_message = AsyncMock()
        a.send = AsyncMock()
        event = types.SimpleNamespace(room_id="!r:example.org", sender="@ignored:example.org", event_id="$1",
                                      content={"msgtype": "m.text", "body": "hello"})
        asyncio.run(a._on_room_message(event))
        prompt = types.SimpleNamespace(requester_user_id="@owner:example.org")
        owner_ok = asyncio.run(a._validate_matrix_prompt_reactor("!r:example.org", "$a", "@owner:example.org", prompt, "approval"))
        other_ok = asyncio.run(a._validate_matrix_prompt_reactor("!r:example.org", "$a", "@other:example.org", prompt, "approval"))
    assert a._handle_text_message.await_count == 0 and owner_ok and not other_ok
