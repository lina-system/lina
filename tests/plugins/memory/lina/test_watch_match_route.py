"""A watch-pattern match is machine output, so both producers that hand it to the agent stamp its route
(``display_kind``) and the memory layer keeps it out of the person's words (lina#20; it replaced Honcho's text filter,
which matched ``[IMPORTANT: Background process ... matched watch pattern``)."""
import queue
from types import SimpleNamespace

from cli import HermesCLI
from plugins.memory.lina.policy import load_policy, signals_for_turn
from tools.process_registry_notifications import format_process_notification
from tui_gateway import server

WATCH = {"type": "watch_match", "session_id": "proc_w", "session_key": "watch-session", "command": "tail -f build.log",
         "pattern": "ERROR", "output": "ERROR: build failed", "suppressed": 0, "event_id": "evt-watch-1"}


def _route(display_kind):
    policy = load_policy()
    turn = [{"role": "user", "content": format_process_notification(WATCH), "display_kind": display_kind}]
    return policy.rule(policy.route_for(signals_for_turn(turn, platform="cli")))


def test_cli_stamps_a_watch_match_as_an_internal_notification(monkeypatch):
    cli = HermesCLI.__new__(HermesCLI)
    cli.session_id = "watch-session"
    cli._pending_input = queue.Queue()
    registry = SimpleNamespace(drain_notifications=lambda **kw: [(WATCH, format_process_notification(WATCH))],
                               completion_queue=queue.Queue(), is_completion_consumed=lambda sid: False)
    monkeypatch.setattr("tools.process_registry.process_registry", registry)
    monkeypatch.setattr("tools.async_delegation.claim_event_delivery", lambda *a: "claimed")
    monkeypatch.setattr("tools.async_delegation.complete_event_delivery", lambda *a: None)
    monkeypatch.setattr("cli.CLI_CONFIG", {"display": {"background_process_notifications": "all"}})
    cli._drain_process_notifications("cli-post-turn")
    queued = cli._pending_input.get_nowait()
    assert queued.display_kind == "internal_notification"
    rule = _route(queued.display_kind)
    assert not rule.honcho


def test_tui_stamps_a_watch_match_as_an_internal_notification(monkeypatch):
    submitted = []
    monkeypatch.setattr("tools.async_delegation.claim_event_delivery", lambda *a: "claimed")
    monkeypatch.setattr("tools.async_delegation.complete_event_delivery", lambda *a: None)
    # session_notifications' bodies run on server.py's globals (method_ctx.bind_module): patch and call there.
    monkeypatch.setattr(server, "_notif_submit", lambda rid, sid, session, text, *a, **kw: submitted.append(kw) or True)
    server._notif_dispatch_event("ui", {}, WATCH, format_process_notification(WATCH))
    assert submitted and submitted[0].get("display_kind") == "internal_notification"
    rule = _route(submitted[0]["display_kind"])
    assert not rule.honcho


def test_an_unstamped_watch_match_would_reach_honcho_as_the_persons_words():
    """The guard the two producers keep: without the stamp the policy table cannot tell."""
    assert _route(None).honcho
