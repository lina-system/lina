"""Manual source contracts. Every response is synthetic and all transports are disconnected."""

import base64
import json
import subprocess
from datetime import UTC, datetime, timedelta
from pathlib import Path
from types import SimpleNamespace

import pytest

from hermes_cli.sources import adapters, mail, slack
from hermes_cli.sources.spool import message_uid
from hermes_cli.sources.state import RoomCheckpoint, SourceState

START = datetime(2026, 1, 1, tzinfo=UTC)
END = START + timedelta(days=1)
ROOT = "1767225601.000001"
REPLY = "1767225602.000002"


def envelope(value):
    return {"result": {"data": {"successful": True, "error": None, "data": json.dumps(value)}}}


def gmail(native):
    body = base64.urlsafe_b64encode(b"Plain line one\nPlain line two").decode()
    return {"messageId": native, "threadId": "thread", "subject": "Subject",
            "messageTimestamp": START.isoformat(), "sender": "Writer <writer@example.test>",
            "display_url": "https://mail.google.com/mail/u/0/#inbox/thread",
            "payload": {"parts": [
                {"mimeType": "multipart/alternative", "parts": [
                    {"mimeType": "text/plain", "body": {"data": body[:8] + "\n" + body[8:]}},
                    {"mimeType": "text/html", "body": {"data": "aHRtbA=="}}]},
                {"filename": "sample.txt", "body": {"data": "PRIVATE-ATTACHMENT", "attachmentId": "unread"}},
            ]}}


def collect(kind, call, config=None, state=None, checkpoint=None, emit=None, *, probe=False, until=END):
    messages = []
    adapters.collect(kind, {"accounts": ["example"]} if config is None else config, START, until,
                     {} if state is None else state.rooms.copy(), call, messages.append if emit is None else emit,
                     checkpoint or (lambda *_: None), probe=probe)
    return messages


def mail_transport(calls):
    def call(tool, params, account):
        calls.append((tool, params, account))
        if tool == mail.DETAIL:
            return envelope(gmail(params["message_id"]))
        if params.get("page_token") == "page-two":
            return envelope({"messages": [{"id": "m2"}]})
        return envelope({"messages": [{"id": "m1"}], "nextPageToken": "page-two"})
    return call


def test_mail_pagination_and_plain_thread_identity():
    calls, checkpoints = [], []
    call = mail_transport(calls)

    messages = collect("mail", call, checkpoint=lambda *args: checkpoints.append(args))
    assert [m["native_id"] for m in messages] == ["m1", "m2"]
    first = messages[0]
    assert (first["room_id"], first["author_id"], first["uid"]) == (
        "example:thread", "writer@example.test", message_uid("mail", "example:thread", "m1"))
    assert first["text"] == "Subject\nPlain line one\nPlain line two"
    assert first["attachments"] == [{"name": "sample.txt"}]
    assert first["display_url"] == gmail("m1")["display_url"]
    assert "PRIVATE-ATTACHMENT" not in json.dumps(messages)
    assert checkpoints == [("example", END.isoformat(), START.isoformat())]
    assert all(account == "example" for _, _, account in calls)
    assert calls[0][1]["include_spam_trash"] is False and "-category:promotions" in calls[0][1]["query"]


def slack_transport(calls):
    def call(tool, params, account):
        calls.append((tool, params.copy()))
        if tool == slack.LIST:
            if "cursor" in params:
                return envelope({"channels": [{"id": "D1", "is_im": True}]})
            return envelope({"channels": [{"id": "foreign", "is_member": False}, {"id": "unknown"},
                                          {"id": "C1", "is_member": True}],
                             "response_metadata": {"next_cursor": "more-rooms"}})
        if tool == slack.HISTORY:
            if params["channel"] == "D1" or "cursor" in params:
                return envelope({"messages": []})
            return envelope({"messages": [{"ts": ROOT, "user": "U1", "text": "root", "reply_count": 1}],
                             "response_metadata": {"next_cursor": "more-history"}})
        if tool == slack.REPLIES:
            if "cursor" not in params:
                return envelope({"messages": [{"ts": ROOT}], "response_metadata": {"next_cursor": "more-thread"}})
            return envelope({"messages": [{"ts": REPLY, "user": "U2", "thread_ts": ROOT,
                                            "text": "&amp; " + "x" * 6000,
                                            "files": [{"name": "file.txt", "url_private": "PRIVATE-FILE"}]}]})
        assert tool == slack.PERMALINK
        return envelope({"permalink": "https://example.slack.com/archives/C1/p" + params["message_ts"].replace(".", "")})
    return call


def test_slack_pages_only_memberships_and_dms_including_thread_replies():
    calls = []
    call = slack_transport(calls)

    messages = collect("slack", call)
    assert [m["native_id"] for m in messages] == [ROOT, REPLY]
    assert messages[1]["thread_root_uid"] == messages[1]["reply_to_uid"] == messages[0]["uid"]
    assert messages[1]["author_id"] == "U2" and messages[1]["room_id"] == "C1"
    assert messages[1]["attachments"] == [{"name": "file.txt"}]
    assert len(messages[1]["text"]) <= adapters.TEXT_LIMIT and messages[1]["text"].startswith("& ")
    assert "PRIVATE-FILE" not in json.dumps(messages)
    assert {p["channel"] for _, p in calls if "channel" in p} == {"C1", "D1"}
    assert {p["message_ts"] for t, p in calls if t == slack.PERMALINK} == {ROOT, REPLY}


@pytest.mark.parametrize(("kind", "tool", "response"), [
    ("mail", mail.LIST, {"messages": [], "nextPageToken": "ignore"}),
    ("slack", slack.LIST, {"channels": [], "response_metadata": {"next_cursor": "ignore"}}),
])
def test_probe_uses_one_read_and_no_checkpoint(kind, tool, response):
    calls = []
    assert collect(kind, lambda t, p, a: calls.append(t) or envelope(response), probe=True,
                   checkpoint=lambda *_: pytest.fail("probe advanced collection")) == []
    assert calls == [tool]


@pytest.mark.parametrize("config", [{"accounts": []}, {"accounts": ["example"], "query": " "}])
def test_empty_mail_configuration_makes_zero_calls(config):
    assert collect("mail", lambda *_: pytest.fail("unexpected read"), config) == []


def test_collectors_dispatch_only_allowed_read_operations(monkeypatch):
    forbidden = {"GMAIL_SEND_EMAIL", "GMAIL_MODIFY_THREAD_LABELS", "GMAIL_GET_ATTACHMENT",
                 "SLACK_SEND_MESSAGE", "SLACK_ADD_REACTION_TO_AN_ITEM", "SLACK_JOIN_AN_EXISTING_CONVERSATION",
                 "SLACK_SET_READ_CURSOR_IN_A_CONVERSATION", "SLACK_LIST_CONVERSATIONS"}
    assert forbidden.isdisjoint(set().union(*adapters.TOOLS.values()))
    for kind, transport in (("mail", mail_transport), ("slack", slack_transport)):
        calls = []
        assert collect(kind, transport(calls))
        dispatched = {call[0] for call in calls}
        assert dispatched and dispatched <= adapters.TOOLS[kind]
        assert dispatched.isdisjoint(forbidden)
    monkeypatch.setattr(adapters, "_composio", lambda *_: pytest.fail("subprocess reached"))
    monkeypatch.setattr(adapters, "_connector", lambda *_: pytest.fail("gateway reached"))
    for kind in ("mail", "slack"):
        for tool in [*forbidden, "arbitrary_read", mail.LIST if kind == "slack" else slack.LIST]:
            with pytest.raises(adapters.SourceError, match="tool_denied"):
                adapters.invoke(kind, tool, {})


def test_composio_uses_noninteractive_argv_and_captures_errors(monkeypatch):
    calls = []
    def run(args, **kwargs):
        calls.append((args, kwargs))
        return SimpleNamespace(returncode=1, stdout=b"", stderr=b"401: PRIVATE-PROVIDER-FIELD")

    monkeypatch.setattr(subprocess, "run", run)
    with pytest.raises(adapters.SourceError) as denied:
        adapters.invoke("mail", mail.LIST, {"query": "space < char"}, "example")
    assert denied.value.auth and str(denied.value) == "auth"
    args, kwargs = calls[0]
    assert isinstance(args, list) and mail.LIST in args and "example" in args
    assert json.dumps({"query": "space < char"}) in args
    assert kwargs["stdin"] == subprocess.DEVNULL and kwargs["capture_output"] and not kwargs.get("shell")
    assert kwargs["env"]["COMPOSIO_CLI_INVOCATION_ORIGIN"] == "run"
    assert len(calls) == 1


@pytest.mark.parametrize(("status", "response", "reason", "auth"), [
    (200, envelope({"ok": False, "error": "account_inactive"}), "auth", True),
    (200, envelope({"ok": False, "error": "missing_scope"}), "auth", True),
    (200, {"connected_account": {"status": "EXPIRED"}}, "inactive_account", True),
    (429, {"message": "private diagnostic"}, "rate_limited", False),
    (503, {"message": "private diagnostic"}, "provider_error", False),
    (400, {"error": {"code": "VALIDATION_ERROR", "message": "tools"}}, "provider_error", False),
])
def test_gateway_one_physical_read_no_retry_fallback_or_provider_logging(monkeypatch, caplog,
                                                                        status, response, reason, auth):
    from tools.connectors.gateway import client

    calls = []
    monkeypatch.setattr(client, "_default_endpoint_resolver", lambda: "https://gateway.example.test")
    monkeypatch.setattr(client, "_default_header_provider", lambda url: {"Authorization": "synthetic"})
    def send(method, url, **kwargs):
        calls.append(kwargs["json"])
        assert kwargs["allow_redirects"] is False
        return SimpleNamespace(status_code=status, json=lambda: response)
    monkeypatch.setattr(client.requests, "request", send)
    with pytest.raises(adapters.SourceError) as error:
        adapters.invoke("slack", slack.LIST, {"limit": 1})
    assert (str(error.value), error.value.auth) == (reason, auth)
    assert len(calls) == 1 and calls[0]["call"]["tool"] == slack.LIST
    assert "private diagnostic" not in caplog.text


def test_mail_resumes_inside_page_after_budget_and_preserves_fixed_window(tmp_path):
    path = tmp_path / "state.json"
    originals = [f"m{i}" for i in range(11)]
    reads, emitted = [], []

    class OutOfBudget(Exception):
        pass

    def run(limit, until):
        state = SourceState.load(path)
        calls = []
        def call(tool, params, account):
            if len(calls) == limit:
                raise OutOfBudget
            calls.append(tool)
            if tool == mail.LIST:
                return envelope({"messages": [{"id": native} for native in originals]})
            reads.append(params["message_id"])
            return envelope(gmail(params["message_id"]))
        collect("mail", call, state=state, checkpoint=RoomCheckpoint(state, path),
                until=until, emit=lambda m: emitted.append(m["native_id"]))

    with pytest.raises(OutOfBudget):
        run(10, END)
    partial = SourceState.load(path).rooms["example"]
    assert "cursor" not in partial and partial["pending"]["until"] == END.isoformat()
    run(10, END + timedelta(hours=1))
    assert emitted == reads == originals
    assert SourceState.load(path).rooms["example"] == {"since": START.isoformat(), "cursor": END.isoformat()}


def test_slack_resumes_inside_thread_then_advances_to_next_dm(tmp_path):
    path = tmp_path / "state.json"
    emitted = []
    last = "1767225603.000003"

    class OutOfBudget(Exception):
        pass

    def run(limit, until):
        state = SourceState.load(path)
        calls = []
        def call(tool, params, account):
            if len(calls) == limit:
                raise OutOfBudget
            calls.append(tool)
            if tool == slack.LIST:
                return {"channels": [{"id": "C1", "is_member": True}, {"id": "D1", "is_im": True}]}
            if tool == slack.HISTORY:
                return {"messages": [] if params["channel"] == "D1" else [{"ts": ROOT, "reply_count": 2}]}
            if tool == slack.REPLIES:
                return {"messages": [{"ts": ROOT}, {"ts": REPLY}, {"ts": last}]}
            assert tool == slack.PERMALINK
            return {"permalink": "https://example.slack.com/archives/C1/p" + params["message_ts"].replace(".", "")}
        collect("slack", call, state=state, checkpoint=RoomCheckpoint(state, path),
                until=until, emit=lambda m: emitted.append(m["native_id"]))

    with pytest.raises(OutOfBudget):
        run(5, END)
    partial = SourceState.load(path).rooms["C1"]
    assert "cursor" not in partial and partial["pending"]["thread_ts"] == ROOT
    run(8, END + timedelta(hours=1))
    assert emitted == [ROOT, REPLY, last]
    assert SourceState.load(path).rooms == {
        room: {"cursor": END.isoformat(), "since": START.isoformat()} for room in ["C1", "D1"]}


def test_message_cap_spans_all_accounts_and_preserves_unread_coverage():
    messages, checkpoints = [], []
    def call(tool, params, account):
        if tool == mail.LIST:
            return {"messages": [{"id": str(i)} for i in range(60)]}
        return gmail(params["message_id"])
    with pytest.raises(adapters.SourceError, match="message_limit"):
        collect("mail", call, {"accounts": ["a", "b"]}, emit=messages.append,
                checkpoint=lambda *args: checkpoints.append(args))
    assert len(messages) == 100 and [room for room, _, _ in checkpoints] == ["a"]
