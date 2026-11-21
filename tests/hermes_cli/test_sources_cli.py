"""A manual source pass through CLI dispatch, provider fixtures, spool, ingest and message reads."""

import json
import sys
from datetime import UTC, datetime

import pytest

from hermes_cli.sources import adapters, mail, runner, slack
from utils import atomic_roundtrip_yaml_update


def test_manual_commands_share_quota_and_visibility(tmp_path, monkeypatch, capsys, memory_store_sql):
    from hermes_cli.main import main

    home = tmp_path / "home"
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("LINA_HOME", str(home))
    config = home / "config.yaml"
    spool = tmp_path / "spool" / "events.jsonl"
    atomic_roundtrip_yaml_update(config, "memory.external_messages", {
        "enabled": True, "spool": str(spool),
        "sources": {"mail": {"enabled": True, "accounts": ["sample"]}, "slack": {"enabled": True}},
    })
    clock = [datetime(2026, 2, 5, tzinfo=UTC).timestamp()]
    monkeypatch.setattr(runner, "time", lambda: clock[0])
    requests = []
    denied = False

    def provider(kind, tool, params, account=None):
        requests.append(tool)
        if denied:
            raise adapters.SourceError("auth", auth=True)
        if tool == mail.LIST:
            return {"messages": [{"id": "mail-one"}]}
        if tool == mail.DETAIL:
            return {"id": "mail-one", "subject": "Cedar newsletter", "internalDate": str(int(clock[0] - 5))}
        if tool == slack.LIST:
            return {"channels": [{"id": "direct", "is_im": True}]}
        assert tool == slack.HISTORY
        return {"messages": [{"ts": str(int(clock[0] - 8)), "text": "Cedar discussion",
                              "permalink": "https://example.test/slack/direct"}]}

    monkeypatch.setattr(adapters, "invoke", provider)

    def command(*words, fails=False):
        monkeypatch.setattr(sys, "argv", ["lina", *words])
        if fails:
            with pytest.raises(SystemExit):
                main()
        else:
            main()
        return capsys.readouterr().out.strip()

    def sources(*words, fails=False):
        return json.loads(command("sources", *words, "--state-dir", str(tmp_path / "state"), fails=fails))

    assert sources("probe", "mail")["calls"] == 1
    assert sources("collect", "mail")["outcome"] == "not_due"
    clock[0] += 3601
    assert sources("collect", "mail", "--dry-run")["messages"] == 1
    assert not spool.exists()
    clock[0] += 3601
    assert sources("collect", "mail")["messages"] == sources("collect", "slack")["messages"] == 1
    command("memory", "external-messages", "ingest")
    assert memory_store_sql("SELECT count(*) FROM memory.messages WHERE source='messages'") == [(2,)]
    hits = json.loads(command("messages", "search", "Cedar"))["results"]
    assert {h["platform"] for h in hits} == {"mail", "slack"}
    for hit in hits:
        opened = json.loads(command("messages", "open", hit["ref"]))
        assert opened["quote_as_external"] and opened["grade"] == "external" and "Cedar" in opened["text"]
    atomic_roundtrip_yaml_update(config, "memory.external_messages.sources.slack.enabled", False)
    assert [h["platform"] for h in json.loads(command("messages", "search", "Cedar"))["results"]] == ["mail"]
    command("messages", "open", next(h["ref"] for h in hits if h["platform"] == "slack"), fails=True)
    clock[0] += 3601
    denied = True
    assert sources("collect", "mail", fails=True)["calls"] == 1
    clock[0] += 3601
    before = len(requests)
    assert sources("probe", "mail", fails=True)["calls"] == 0
    assert len(requests) == before
    assert sources("status")["mail"]["stopped"]
    assert sources("resume", "mail")["outcome"] == "ready"
    assert not sources("status")["mail"]["stopped"]
