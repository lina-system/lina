"""``/api/lina/work/settings`` (lina#256) over a scratch LINA root: served JSON validated against
``docs/api/settings-client.yaml``, ETag and If-Match (428, 412), 422 with paths, the owner-device write gate through a
fake ``tailscale whois`` (the real one never runs), 30 writes a minute per device, and the ``settings`` source on the
change stream. Every login, device and model is synthetic."""

from __future__ import annotations

import json
import stat
import sys
import textwrap
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import lina_magpie

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
BASE = "/api/lina/work/settings"
OWNER = "owner@example.test"
FAKE_TAILSCALE = textwrap.dedent("""\
    #!{python}
    import json, sys
    from pathlib import Path
    table = json.loads((Path(__file__).parent / "whois.json").read_text())
    peer = sys.argv[3]
    if sys.argv[1:3] != ["whois", "--json"] or peer not in table:
        sys.stderr.write("no such peer\\n")
        sys.exit(1)
    login, device = table[peer]
    print(json.dumps({{"Node": {{"ComputedName": device, "Name": device + ".tailnet.example."}},
                      "UserProfile": {{"LoginName": login}}}}))
    """)


def _openapi_to_jsonschema(node):
    if isinstance(node, list):
        return [_openapi_to_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _openapi_to_jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"] if isinstance(out["type"], str) else [*out["type"], "null"]
    elif node.get("nullable"):
        out = {"anyOf": [out, {"type": "null"}]}
    return out


_SPEC = hermes_yaml.safe_load((REPO / "docs/api/settings-client.yaml").read_text(encoding="utf-8"))
_COMPONENTS = _openapi_to_jsonschema(_SPEC["components"])


def _valid(schema_name: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{schema_name}", "components": _COMPONENTS}).validate(data)
    return data


def _config(owner: bool, tool: Path) -> str:
    gate = (f"  settings:\n    owner: {{login: {OWNER}, devices: [phone-a, laptop-a]}}\n    tailscale: {tool}\n"
            if owner else "")
    return textwrap.dedent("""\
        devices:
          workers:
            build-a: {host: build-a, cap: 2}
            here: {host: local, cap: 1}
        worker:
          runners:
            codex: {type: command, argv: [codex, "-"], model: openai/m-open}
          routing:
            - {name: docs, when: {labels_any: [docs]}, runner: codex}
        """) + gate


class Env:
    def __init__(self, tmp_path: Path, monkeypatch):
        self.root = tmp_path / "lina"
        self.root.mkdir()
        self.tool = tmp_path / "bin" / "tailscale"
        self.tool.parent.mkdir()
        self.tool.write_text(FAKE_TAILSCALE.format(python=sys.executable), encoding="utf-8")
        self.tool.chmod(self.tool.stat().st_mode | stat.S_IXUSR)
        self.whois({"testclient": (OWNER, "phone-a")})
        self.owner(True)
        monkeypatch.setenv("LINA_HOME", str(self.root))
        monkeypatch.setattr(lina_magpie, "get", lambda proxy, path: {"data": [{"id": "hub/openai/m-open"}]})
        monkeypatch.setattr(lina_magpie, "quota_accounts", lambda proxy: [
            {"provider": "openai", "id": "a1", "quota": {"weeklyPercent": 25, "weeklyResetAt": 1_700_000_000}}])
        from hermes_cli import lina_work_settings_gate, lina_work_settings_models
        lina_work_settings_gate.GATE.reset()
        lina_work_settings_models.forget()

    def whois(self, table: dict) -> None:
        (self.tool.parent / "whois.json").write_text(json.dumps(table), encoding="utf-8")

    def owner(self, on: bool) -> None:
        (self.root / "config.yaml").write_text(_config(on, self.tool), encoding="utf-8")

    def config(self) -> dict:
        return hermes_yaml.safe_load((self.root / "config.yaml").read_text(encoding="utf-8"))


@pytest.fixture
def env(tmp_path, monkeypatch):
    yield Env(tmp_path, monkeypatch)
    from tests.hermes_cli._work_settings_scan import untyped_runners
    assert untyped_runners(tmp_path) == []


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _get(client) -> tuple[dict, str]:
    reply = client.get(BASE)
    assert reply.status_code == 200, reply.text
    return _valid("SettingsView", reply.json()), reply.headers["ETag"]


def test_get_serves_the_settings_with_an_etag_and_a_matching_if_none_match_answers_304(env, client):
    view, etag = _get(client)
    assert view["revision"]["id"] == "r1" and view["writable"] is True
    assert view["settings"]["devices"]["workers"]["build-a"]["cap"] == 2
    assert "settings" not in view["settings"]["worker"]  # the gate's own config is not a work setting
    assert client.get(BASE, headers={"If-None-Match": etag}).status_code == 304


def test_patch_needs_if_match_428_refuses_a_stale_one_412_and_applies_a_merge_patch(env, client):
    _, etag = _get(client)
    patch = {"devices": {"workers": {"build-a": {"cap": 3}}},
             "worker": {"runners": {"codex": {"model": "openai/m-open:high"}}}}
    missing = client.patch(BASE, json=patch)
    assert missing.status_code == 428 and _valid("Detail", missing.json())
    done = client.patch(BASE, json=patch, headers={"If-Match": etag})
    assert done.status_code == 200, done.text
    result = _valid("WriteResult", done.json())
    assert result["revision"]["id"] == "r2" and done.headers["ETag"] != etag
    assert {c["path"] for c in result["changes"]} == {"devices.workers.build-a.cap", "worker.runners.codex.model"}
    assert env.config()["devices"]["workers"]["build-a"]["cap"] == 3
    stale = client.patch(BASE, json={"devices": {"workers": {"here": {"cap": 2}}}}, headers={"If-Match": etag})
    assert stale.status_code == 412 and stale.headers["ETag"] == done.headers["ETag"]
    removed = client.patch(BASE, json={"worker": {"routing": None}}, headers={"If-Match": done.headers["ETag"]})
    assert removed.status_code == 200 and "routing" not in env.config()["worker"]


def test_an_invalid_patch_is_422_with_paths_and_writes_nothing(env, client):
    _, etag = _get(client)
    before = (env.root / "config.yaml").read_bytes()
    reply = client.patch(BASE, json={"devices": {"workers": {"laptop": {"role": "person", "cap": 1}}},
                                     "worker": {"runners": {"codex": {"env": {"A": "b"}, "api_token": "x"}}}},
                         headers={"If-Match": etag})
    assert reply.status_code == 422
    body = _valid("ValidationError", reply.json())
    assert {e["path"] for e in body["errors"]} >= {"devices.workers.laptop.cap", "worker.runners.codex.env",
                                                    "worker.runners.codex.api_token"}
    assert (env.root / "config.yaml").read_bytes() == before


@pytest.mark.parametrize("patch", [{"worker": "broken"}, {"devices": []}])
def test_a_non_mapping_section_is_422_with_its_path_and_the_body_holds_only_validator_text(env, client, patch):
    _, etag = _get(client)
    before = (env.root / "config.yaml").read_bytes()
    reply = client.patch(BASE, json=patch, headers={"If-Match": etag})
    assert reply.status_code == 422
    body = _valid("ValidationError", reply.json())
    first = body["errors"][0]
    assert first["path"] == next(iter(patch)) and body["detail"].startswith(f"{first['path']}: {first['message']}")
    assert (env.root / "config.yaml").read_bytes() == before


@pytest.mark.parametrize(("patch", "where"), [
    ({"worker": {"routing": [{"name": [], "runner": "omo"}]}}, "worker.routing[0].name"),
    ({"worker": {"routing": [{"name": "r", "runner": ["omo"]}]}}, "worker.routing[0].runner"),
    ({"worker": {"routing": [{"name": "r", "runner": "omo", "when": {"size": [{"S": 1}]}}]}},
     "worker.routing[0].when.size"),
    ({"worker": {"runners": {"codex": {"type": ["command"]}}}}, "worker.runners.codex.type"),
    ({"devices": {"workers": {"here": {"role": {"a": 1}}}}}, "devices.workers.here.role"),
    ({"worker": {"default_runner": ["omo"]}}, "worker.default_runner"),
])
def test_a_wrong_type_is_422_with_its_path_never_a_500(env, client, patch, where):
    _, etag = _get(client)
    reply = client.patch(BASE, json=patch, headers={"If-Match": etag})
    assert reply.status_code == 422, reply.text
    assert where in [e["path"] for e in _valid("ValidationError", reply.json())["errors"]]


def test_removing_a_runner_that_holds_env_is_422_and_writes_nothing(env, client):
    path = env.root / "config.yaml"
    path.write_text(path.read_text(encoding="utf-8").replace(
        'model: openai/m-open}', 'model: openai/m-open, env: {SOME_HOME: /srv/x}}'), encoding="utf-8")
    _, etag = _get(client)
    before = path.read_bytes()
    reply = client.patch(BASE, json={"worker": {"runners": {"codex": None}, "routing": None}},
                         headers={"If-Match": etag})
    assert reply.status_code == 422
    body = _valid("ValidationError", reply.json())
    assert body["errors"][0]["path"] == "worker.runners.codex" and "(env)" in body["errors"][0]["message"]
    assert path.read_bytes() == before


def test_credential_like_keys_written_by_hand_never_show_in_settings_or_history(env, client):
    path = env.root / "config.yaml"
    text = path.read_text(encoding="utf-8")
    text = text.replace("build-a: {host: build-a, cap: 2}", "build-a: {host: build-a, cap: 2, password: tok-dev-3}")
    text = text.replace("model: openai/m-open}", "model: openai/m-open, auth_token: tok-run-4}")
    text = text.replace("runner: codex}", "runner: codex, secret_note: tok-route-2}")
    text = text.replace("  routing:", "  models:\n    aliases: {planner: {provider: p, id: i, api_token: tok-alias-1}}\n  routing:")
    path.write_text(text, encoding="utf-8")
    bodies = [client.get(BASE).text, client.get(f"{BASE}/history").text, client.get(f"{BASE}/history/r1").text]
    for body in bodies:
        for word in ("tok-dev-3", "tok-run-4", "tok-route-2", "tok-alias-1", "password", "auth_token", "secret_note",
                     "api_token"):
            assert word not in body, (word, body[:200])
    assert "tok-alias-1" in path.read_text(encoding="utf-8")


@pytest.mark.parametrize("labels", [False, 0, "", {}, "docs"])
def test_route_answers_400_for_labels_that_are_present_but_not_a_list(env, client, labels):
    assert client.post(f"{BASE}/route", json={"labels": labels}).status_code == 400
    assert client.post(f"{BASE}/route", json={}).status_code == 200  # absent labels: no labels


def test_the_cli_the_api_and_lina_devices_write_the_one_config_the_worker_reads(env, client):
    """LINA_HOME (the worker's root, lina#227) and lina_deps.lina_root() differ here, as they can in production."""
    import argparse

    from hermes_cli import lina_devices, lina_deps
    from hermes_cli.subcommands.worker import build_worker_parser
    from hermes_cli.worker import settings
    assert lina_deps.lina_root() != env.root and settings.lina_home() == env.root
    parser = argparse.ArgumentParser(prog="lina")
    build_worker_parser(parser.add_subparsers(dest="command"))
    args = parser.parse_args(["worker", "settings", "set", "devices.workers.cli-dev", "{host: cli-dev}"])
    with pytest.raises(SystemExit) as done:
        args.func(args)
    assert done.value.code == 0
    _, etag = _get(client)
    reply = client.patch(BASE, json={"devices": {"workers": {"api-dev": {"host": "api-dev"}}}}, headers={"If-Match": etag})
    assert reply.status_code == 200, reply.text
    lina_devices.add("add-dev", "add-dev", out=lambda _line: None)
    names = {dev.name for dev in settings.devices()}
    assert {"cli-dev", "api-dev", "add-dev"} <= names
    assert settings.section().get("settings")  # the same file also holds the write gate's owner
    assert not (lina_deps.lina_root() / "config.yaml").exists()


def test_an_unexpected_failure_is_a_500_without_its_message(env, monkeypatch):
    from hermes_cli import lina_work_settings_store, web_server

    def boom(*_args, **_kwargs):
        raise RuntimeError("internal-detail-7f3a /srv/secret")
    monkeypatch.setattr(lina_work_settings_store.Store, "apply", boom)
    client = TestClient(web_server.app, raise_server_exceptions=False)
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    _, etag = _get(client)
    reply = client.patch(BASE, json={"devices": {"workers": {"here": {"cap": 2}}}}, headers={"If-Match": etag})
    assert reply.status_code == 500
    assert "internal-detail-7f3a" not in reply.text and "/srv/secret" not in reply.text


def test_history_one_revision_and_two_rollbacks_return_to_the_start(env, client):
    start, etag = _get(client)
    etag = client.patch(BASE, json={"devices": {"workers": {"here": {"cap": 2}}}},
                        headers={"If-Match": etag}).headers["ETag"]
    assert client.post(f"{BASE}/rollback").status_code == 428
    once = client.post(f"{BASE}/rollback", headers={"If-Match": etag})
    assert once.status_code == 200 and _valid("WriteResult", once.json())["revision"]["id"] == "r3"
    twice = client.post(f"{BASE}/rollback", headers={"If-Match": once.headers["ETag"]})
    assert twice.status_code == 200 and env.config()["devices"]["workers"]["here"]["cap"] == 2
    history = _valid("HistoryView", client.get(f"{BASE}/history?limit=3").json())
    assert [line["rev"] for line in history["revisions"]] == ["r4", "r3", "r2"]
    assert history["revisions"][0]["by"] == {"kind": "rollback", "principal": OWNER, "device": "phone-a"}
    one = _valid("RevisionView", client.get(f"{BASE}/history/r1").json())
    assert one["settings"] == start["settings"]
    assert client.get(f"{BASE}/history/r99").status_code == 404


def test_models_and_route_are_served_in_their_schemas(env, client):
    models = _valid("ModelsView", client.get(f"{BASE}/models").json())
    assert models["models"][0]["left"] == 75 and models["errors"] == []
    routed = client.post(f"{BASE}/route", json={"labels": ["docs"], "size": "M"})
    assert routed.status_code == 200 and _valid("RouteView", routed.json())["runner"] == "codex"
    assert client.post(f"{BASE}/route", json={"labels": "docs"}).status_code == 400


# ---------------------------------------------------------------- the write gate
@pytest.mark.parametrize("who", [
    {"testclient": ("someone@example.test", "phone-a")},  # another login
    {"testclient": (OWNER, "desk-z")},  # the owner, on a device not in the list
    {},  # whois fails
])
def test_a_write_from_anyone_but_the_owner_s_listed_device_is_refused_and_reads_go_on(env, client, who):
    env.whois(who)
    view, etag = _get(client)
    assert view["writable"] is False
    reply = client.patch(BASE, json={"devices": {"workers": {"here": {"cap": 2}}}}, headers={"If-Match": etag})
    assert reply.status_code == 403 and _valid("Detail", reply.json())
    assert client.post(f"{BASE}/rollback", headers={"If-Match": etag}).status_code == 403
    assert env.config()["devices"]["workers"]["here"]["cap"] == 1


def test_no_owner_configured_means_nobody_writes_through_the_api(env, client):
    env.owner(False)
    view, etag = _get(client)
    assert view["writable"] is False
    reply = client.patch(BASE, json={"devices": {"workers": {"here": {"cap": 2}}}}, headers={"If-Match": etag})
    assert reply.status_code == 403


def test_without_the_dashboard_credential_nothing_is_served(env):
    from hermes_cli import web_server
    anonymous = TestClient(web_server.app)
    assert anonymous.get(BASE).status_code == 401
    assert anonymous.patch(BASE, json={}, headers={"If-Match": '"x"'}).status_code == 401


def test_a_device_makes_at_most_30_writes_a_minute(env, client):
    _, etag = _get(client)
    for n in range(30):
        reply = client.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": n + 3}}}},
                             headers={"If-Match": etag})
        assert reply.status_code == 200, (n, reply.text)
        etag = reply.headers["ETag"]
    over = client.patch(BASE, json={"devices": {"workers": {"build-a": {"cap": 99}}}}, headers={"If-Match": etag})
    assert over.status_code == 429
    assert env.config()["devices"]["workers"]["build-a"]["cap"] == 32


# ---------------------------------------------------------------- change stream
def test_the_change_stream_carries_settings_revisions_rollbacks_and_outside_edits(env, client):
    from hermes_cli import web_server
    url = f"/api/lina/events?token={web_server._SESSION_TOKEN}"
    _, etag = _get(client)
    with ThreadPoolExecutor(max_workers=1) as pool, client.websocket_connect(url) as ws:
        assert pool.submit(ws.receive_json).result(timeout=5)["events"] == []
        etag = client.patch(BASE, json={"devices": {"workers": {"here": {"cap": 2}}}},
                            headers={"If-Match": etag}).headers["ETag"]
        client.post(f"{BASE}/rollback", headers={"If-Match": etag})
        path = env.root / "config.yaml"
        path.write_text(path.read_text(encoding="utf-8").replace("cap: 2}", "cap: 7}", 1), encoding="utf-8")
        _get(client)
        seen: list[dict] = []
        while len(seen) < 3:
            message = pool.submit(ws.receive_json).result(timeout=5)
            seen += [_valid("SettingsChangeEvent", e) for e in message.get("events", []) if e["source"] == "settings"]
    assert [(e["kind"], e["id"]) for e in seen] == [("revised", "r2"), ("rolled_back", "r3"), ("outside", "r4")]
