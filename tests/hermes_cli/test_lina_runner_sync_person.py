"""The person-device lines of the old daily drift check (lina#490): the device's magpie, the address its tools point
at, Claude's self-update and the app-managed runner copies, read on a person's device without writing to it or
moving a secret off it, listed by ``sync status --issues`` and started with the day's first daily sync."""
from __future__ import annotations

import json
import os
import re
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from hermes_cli import lina_deps, lina_runner_sync, lina_runner_sync_daily, lina_runners
from hermes_cli import lina_runner_sync_person as person
from hermes_cli import lina_runner_sync_state as state
from tests.hermes_cli.test_lina_runner_sync_node import _Ended, _local

URL = "http://127.0.0.1:3425/v1/models"
LINKED = {"health": 200, "models": 3}
LOCAL = "http://127.0.0.1:3425"
QUIET = {"claude env.ANTHROPIC_BASE_URL": LOCAL, "aside providers.magpie.baseUrl": f"{LOCAL}/v1",
         "claude env.DISABLE_AUTOUPDATER": "1"}
SECRETS = ("s3cret", "who", "tok-123", "frag", "sk-ant-api-xyz", "gw-key-9")
NOTHING = {"issues": [], "manual": [], "notes": []}


class _Models(BaseHTTPRequestHandler):
    body = json.dumps({"object": "list", "data": [{"id": "hub/openai/gpt"}, {"id": "hub/anthropic/claude"}]})

    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(self.body.encode())

    def log_message(self, *args):
        pass


def _tool(path: Path, version: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(f"#!/bin/sh\necho 'tool {version}'\n", encoding="utf-8")
    path.chmod(0o755)


def _tree(top: Path) -> dict:
    return {str(p): (p.lstat().st_mtime_ns, p.read_bytes() if p.is_file() and not p.is_symlink() else None)
            for p in sorted(top.rglob("*"))}


def _seen(link: dict | None = None, **configs) -> dict:
    return {"reachable": True, "tools": {}, "link": link or LINKED, "configs": {**QUIET, **configs}}


@pytest.fixture
def root(tmp_path, monkeypatch):
    base = tmp_path / "hub"
    base.mkdir()
    monkeypatch.setattr(lina_deps, "lina_root", lambda: base)
    return base


def _pin(root: Path, name: str) -> str:
    return lina_runners.pins(root=root)[name].version


def _read_device(device: Path, url: str) -> dict:
    env = {key: value for key, value in os.environ.items() if key != "OMO_CODING_AGENT_DIR"}
    done = subprocess.run(["/bin/sh", "-c", person.script(["codex", "grok", "agy"], url)], capture_output=True,
                          text=True, timeout=30, check=True,
                          env={**env, "HOME": str(device), "PATH": f"{device / 'bin'}:/usr/bin:/bin"})
    return {"reachable": True, **person.parse(done.stdout), "stdout": done.stdout}


@pytest.mark.platforms("linux")
def test_the_read_writes_nothing_on_the_device_and_carries_no_secret_off_it(root, tmp_path):
    device = tmp_path / "device"
    bundled = device / "Applications" / "Codex.app" / "Contents" / "Resources" / "codex"
    _tool(bundled, "0.1.0")
    _tool(device / "bin" / "grok", "0.0.1")
    (device / "bin" / "codex").symlink_to(bundled)
    (device / ".claude").mkdir()
    (device / ".claude" / "settings.json").write_text(json.dumps({"env": {
        "ANTHROPIC_BASE_URL": "https://who:s3cret@proxy.example:8443/v1?key=tok-123#frag",
        "ANTHROPIC_API_KEY": "sk-ant-api-xyz", "OTHER_KEY": "gw-key-9"}}), encoding="utf-8")
    server = ThreadingHTTPServer(("127.0.0.1", 0), _Models)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{server.server_address[1]}/v1/models"
    before = _tree(device)
    try:
        seen = _read_device(device, url)
    finally:
        server.shutdown()
        server.server_close()

    assert _tree(device) == before, "the read changes nothing on the device"
    output = seen.pop("stdout")
    assert not [secret for secret in SECRETS if secret in output], "no secret leaves the device"
    assert seen["link"] == {"health": 200, "models": 2}
    assert seen["configs"] == {"claude env.ANTHROPIC_BASE_URL": "https://proxy.example:8443/v1",
                               "aside providers.magpie.baseUrl": "none", "claude env.DISABLE_AUTOUPDATER": "absent"}
    assert seen["tools"]["codex"]["version"] == "0.1.0" and seen["tools"]["agy"] == {}
    where = "on mac (~/.claude/settings.json env."
    assert person.judge("mac", seen, person.tools(root), url) == {
        "issues": [f"- tool address: claude {where}ANTHROPIC_BASE_URL) points at https://proxy.example:8443/v1, "
                   "not the local magpie",
                   f"- auto-update: claude {where}DISABLE_AUTOUPDATER) is absent: it updates itself away from the "
                   "hub's version"],
        "notes": ["- tool address: aside on mac (~/.omo/agent/models.json providers.magpie.baseUrl) none"],
        "manual": [f"- manual: codex on mac 0.1.0, hub {_pin(root, 'codex')}: managed by Codex.app, update it in "
                   "the app"]}


@pytest.mark.platforms("linux")
def test_an_unreadable_settings_file_is_unknown(root, tmp_path):
    device = tmp_path / "device"
    (device / ".claude").mkdir(parents=True)
    (device / ".claude" / "settings.json").write_text("{not json", encoding="utf-8")
    seen = _read_device(device, "http://127.0.0.1:9/v1/models")
    assert seen["configs"]["claude env.ANTHROPIC_BASE_URL"] == "unknown"
    assert seen["link"]["health"] is None


def test_the_script_sends_no_key_and_writes_only_to_dev_null():
    text = person.script(["codex", "claude"], URL)
    assert not re.search(r"authorization|api_?key|bearer|-H\b|key_env|token", text, re.IGNORECASE)
    assert {target for target in re.findall(r">\s*([^\s;)\"]+)", text)} <= {"/dev/null", "&1"}
    assert re.findall(r"curl [^;]*", text) == [f"curl -s -m 5 -w '\\n@@code %{{http_code}}\\n' {URL} 2>/dev/null"]
    read_keys = re.findall(r"\b_[uf] \"[^\"]+\" ([^\s;]+)", text)
    assert read_keys == [key for _tool_name, _path, key, _kind in person.CONFIGS]


@pytest.mark.parametrize(("link", "kind", "line"), [
    ({"health": 200, "models": 0}, "issues", f"- proxy: mac model path lists no model (magpie {URL}: HTTP 200, 0 models)"),
    ({"health": 502, "models": 0}, "issues", f"- proxy: mac model path does not answer (magpie {URL}: HTTP 502)"),
    ({"health": None, "models": 0}, "notes", f"- magpie: mac unknown (no answer at {URL})"),
    ({"health": 401, "models": 0}, "notes", f"- magpie: mac unknown (HTTP 401 at {URL}: it wants a key)"),
    ({"health": 403, "models": 0}, "notes", f"- magpie: mac unknown (HTTP 403 at {URL}: it wants a key)"),
])
def test_the_device_magpie_is_an_issue_only_when_it_answers_wrong(root, link, kind, line):
    lines = person.judge("mac", _seen(link), person.tools(root), URL)
    assert lines[kind] == [line] and sum(map(len, lines.values())) == 1


ADDRESS = "claude env.ANTHROPIC_BASE_URL"
SHOWN = "- tool address: claude on mac (~/.claude/settings.json env.ANTHROPIC_BASE_URL)"


@pytest.mark.parametrize(("value", "kind", "line"), [
    ("http://localhost:3425/", None, None),
    ("http://[::1]:3425/v1", None, None),
    ("http://127.0.0.1:3426", "issues", f"{SHOWN} points at http://127.0.0.1:3426, not the local magpie"),
    ("https://[2001:db8::1]:3425/v1", "issues", f"{SHOWN} points at https://[2001:db8::1]:3425/v1, not the local magpie"),
    ("absent", "issues", f"{SHOWN} not set: it does not go through the local magpie"),
    ("none", "notes", f"{SHOWN} none"),
    ("unknown", "notes", f"{SHOWN} unknown"),
    ("other", "notes", f"{SHOWN} unknown (not an http address)"),
])
def test_a_tool_address_is_ok_an_issue_or_unknown(root, value, kind, line):
    lines = person.judge("mac", _seen(**{ADDRESS: value}), person.tools(root), URL)
    assert lines == (NOTHING if kind is None else {**NOTHING, kind: [line]})


@pytest.mark.parametrize(("value", "kind"), [("1", None), ("0", "issues"), ("absent", "issues"), ("none", "notes"),
                                             ("unknown", "notes")])
def test_claudes_self_update_is_an_issue_unless_it_is_off(root, value, kind):
    lines = person.judge("mac", _seen(**{"claude env.DISABLE_AUTOUPDATER": value}), person.tools(root), URL)
    assert [key for key, found in lines.items() if found] == ([] if kind is None else [kind])


@pytest.mark.parametrize(("address", "shown"), [
    ("http://[::1]:3425/v1", "http://[::1]:3425/v1"),
    ("https://user:pw@[fe80::1]:8443/x?k=v#f", "https://[fe80::1]:8443/x"),
    ("http://who:s3cret@127.0.0.1:3425/v1?key=tok#frag", "http://127.0.0.1:3425/v1"),
    ("ftp://127.0.0.1/", None), ("not a url", None), ("http://127.0.0.1:99999/", None),
])
def test_safe_endpoint_keeps_ipv6_brackets_and_drops_credentials(address, shown):
    assert person.safe_endpoint(address) == shown
    if shown:
        assert person.safe_endpoint(shown) == shown


def test_an_app_copy_on_the_pin_or_a_copy_outside_an_app_is_no_line(root):
    pinned = person.tools(root)
    seen = _seen()
    seen["tools"] = {"codex": {"path": "/usr/local/bin/codex", "real": "/Applications/Codex.app/Contents/Resources/codex",
                               "version": _pin(root, "codex")},
                     "grok": {"path": "/Users/p/.local/bin/grok", "real": "/Users/p/.grok/grok", "version": "0.0.1"}}
    assert person.judge("mac", seen, pinned, URL) == NOTHING
    assert person.judge("mac", {"reachable": False}, pinned, URL) == {**NOTHING, "notes": ["- unreachable: mac"]}


def _devices(root: Path, workers: dict) -> None:
    (root / "config.yaml").write_text(json.dumps({"worker": {"runner_sync": {"at": "04:00"}},
                                                  "devices": {"workers": workers}}), encoding="utf-8")


def test_the_check_reads_only_person_devices_and_sends_one_notice(root):
    _devices(root, {"build": {"host": "build-1"}, "mac": {"host": "mac-alias", "role": "person"}})
    read: list[str] = []
    notices: list[str] = []
    app_copy = {"path": "/Applications/Codex.app/Contents/Resources/codex", "version": "0.1.0"}

    def reader(alias, text):
        read.append(alias)
        seen = _seen({"health": 502, "models": 0})
        seen["tools"] = {"codex": app_copy}
        return seen

    assert person.run(root, reader=reader, notify=lambda root, text: notices.append(text) or "kept",
                      now=_local(0, 4)) == 0

    assert read == ["mac-alias"]
    saved = json.loads((state.sync_dir(root) / person.PERSON_FILE).read_text(encoding="utf-8"))
    assert saved["issues"] == [f"- proxy: mac model path does not answer (magpie {URL}: HTTP 502)"]
    assert len(saved["manual"]) == 1 and saved["devices"]["mac"]["reachable"] is True
    assert notices == ["\n".join([f"person devices {state.day(_local(0, 4))}:", *saved["issues"],
                                  *saved["manual"]]) + "\n"]


def test_a_quiet_person_device_sends_no_notice(root):
    _devices(root, {"mac": {"host": "mac-alias", "role": "person"}})
    notices: list[str] = []
    person.run(root, reader=lambda alias, text: _seen(),
               notify=lambda root, text: notices.append(text) or "kept")
    assert notices == []


def _issues(root: Path) -> list[str]:
    printed: list[str] = []
    lina_runner_sync.status(root, issues_only=True, out=printed.append)
    return printed


def test_sync_status_issues_lists_the_person_devices_issues_and_manual_lines(root):
    _devices(root, {"mac": {"host": "mac-alias", "role": "person"}})
    seen = _seen({"health": None, "models": 0}, **{ADDRESS: "http://10.0.0.5:3425"})
    seen["tools"] = {"codex": {"path": "/Applications/Codex.app/Contents/Resources/codex", "version": "0.1.0"}}
    person.run(root, reader=lambda alias, text: seen, notify=lambda root, text: "kept")

    assert _issues(root) == [f"{SHOWN} points at http://10.0.0.5:3425, not the local magpie",
                             f"- manual: codex on mac 0.1.0, hub {_pin(root, 'codex')}: managed by Codex.app, "
                             "update it in the app"]
    assert lina_runner_sync.issue_lines(root)[1] == [f"- magpie: mac unknown (no answer at {URL})"]


def test_sync_status_issues_shows_nothing_for_a_quiet_person_device(root):
    _devices(root, {"mac": {"host": "mac-alias", "role": "person"}})
    person.run(root, reader=lambda alias, text: _seen(), notify=lambda root, text: "kept")
    assert _issues(root) == ["- none"]


@pytest.fixture
def tick(root, monkeypatch):
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)
    monkeypatch.setattr(lina_runner_sync_daily, "_person", None)
    starts: list[list[str] | None] = []
    checks: list[Path] = []

    def at(now: float) -> bool:
        return lina_runner_sync_daily.maybe_start(root / "worker", now=now,
                                                  spawn=lambda base, only=None: starts.append(only) or _Ended(),
                                                  person_spawn=lambda base: checks.append(base) or _Ended())
    return at, starts, checks


def test_the_days_first_sync_starts_the_person_check_and_the_retry_does_not(root, tick):
    at, starts, checks = tick
    _devices(root, {"mac": {"host": "mac-alias", "role": "person"}})

    assert at(_local(0, 4)) is True
    state.write_json(state.last_path(root), {**state.last(root), "finished": _local(0, 4), "retry": True,
                                             "busy": ["grok"]})
    assert at(_local(0, 4, 31)) is True

    assert starts == [None, ["grok"]] and checks == [root]


def test_a_hub_without_person_devices_starts_no_person_check(root, tick):
    at, starts, checks = tick
    _devices(root, {"build": {"host": "build-1"}})

    assert at(_local(0, 4)) is True
    assert starts == [None] and checks == []
