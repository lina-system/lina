"""``lina devices sign-in <node> <runner>`` (lina#230) end to end on this machine: a fake ``ssh`` runs the node side
under its own HOME and TMPDIR, a fake runner CLI listens on a real 127.0.0.1 port and hands its sign-in address to
``$BROWSER``, and a fake browser command follows the address back to that listener (or prints a paste code, or stops
for a person). The inbox is a scratch sqlite work record read the way the board reads it.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import textwrap
from pathlib import Path

import pytest

from hermes_cli import lina_board, lina_devices, lina_sign_in
from tests.hermes_cli._work_env import WorkEnv

pytestmark = pytest.mark.platforms("posix")

CODE = "c0de-5ecret-7f3a"
STATE = "st4te-9b1e"
NODE = "node-a"

FAKE_SSH = """\
#!{python}
import json, os, sys
args = sys.argv[1:]
with open(os.environ["FAKE_SSH_LOG"], "a", encoding="utf-8") as log:
    log.write(json.dumps(args) + "\\n")
i = 0
while args[i].startswith("-"):
    i += 2 if args[i] in ("-o", "-L") else 1
env = dict(os.environ, HOME=os.environ["FAKE_NODE_HOME"], TMPDIR=os.environ["FAKE_NODE_TMP"])
os.execvpe("sh", ["sh", "-c", args[i + 1]], env)
"""

FAKE_GROK = """\
#!{python}
import http.server, os, pathlib, subprocess, sys, urllib.parse
home = pathlib.Path(os.environ["HOME"])
(home / "cli.pid").write_text(str(os.getpid()))
got = []

class Back(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        query = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
        got.append(query.get("code") == [{code!r}] and query.get("state") == [{state!r}])
        self.send_response(200)
        self.end_headers()

    def log_message(self, *args):
        pass

server = http.server.HTTPServer(("127.0.0.1", 0), Back)
back = "http://127.0.0.1:%d/callback" % server.server_address[1]
address = "https://auth.example.test/authorize?" + urllib.parse.urlencode(
    {{"client_id": "fake", "state": {state!r}, "redirect_uri": back}})
print("Open this URL to sign in:", address)
print(address, file=sys.stderr)
subprocess.Popen([os.environ["BROWSER"], address])
while not got:
    server.handle_request()
if got[0]:
    (home / ".grok").mkdir(exist_ok=True)
    (home / ".grok" / "auth.json").write_text("{{}}")
sys.exit(0 if got[0] else 1)
"""

FAKE_AGY = """\
#!{python}
import os, pathlib, subprocess, sys, time
home = pathlib.Path(os.environ["HOME"])
(home / "cli.pid").write_text(str(os.getpid()))
sys.stdin.readline()
address = "https://accounts.example.test/o/oauth2/auth?" + "state={state}&redirect_uri=https%3A%2F%2Fpage.example.test%2Fcode"
subprocess.Popen([os.environ["BROWSER"], address])
print("paste the authorization code here")
if sys.stdin.readline().strip() == {code!r}:
    (home / ".gemini" / "antigravity-cli").mkdir(parents=True)
    (home / ".gemini" / "antigravity-cli" / "antigravity-oauth-token").write_text("{{}}")
while True:
    time.sleep(60)
"""

FAKE_BROWSER = """\
#!{python}
import json, os, sys, urllib.parse, urllib.request
mode = sys.argv[1]
address = sys.stdin.readline().strip()
with open(os.environ["FAKE_BROWSER_SEEN"], "w", encoding="utf-8") as seen:
    json.dump({{"argv": sys.argv[1:], "account": os.environ.get("LINA_SIGN_IN_ACCOUNT"),
               "return": os.environ.get("LINA_SIGN_IN_RETURN"), "got_address": bool(address),
               "secret": "GITHUB_TOKEN" in os.environ}}, seen)
print("browser opened " + address, file=sys.stderr)
if mode in ("two_step", "new_device", "account_choice"):
    print("needs_person: " + mode, file=sys.stderr)
    sys.exit(3)
if mode == "code":
    print({code!r})
    sys.exit(0)
back = urllib.parse.parse_qs(urllib.parse.urlsplit(address).query)["redirect_uri"][0]
urllib.request.urlopen(back + "?" + urllib.parse.urlencode({{"code": {code!r}, "state": {state!r}}}), timeout=30).read()
"""


def _script(path: Path, template: str) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(template.format(python=sys.executable, code=CODE, state=STATE), encoding="utf-8")
    path.chmod(0o755)
    return path


def _alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    return True


class Hub:
    """The scratch hub (its LINA root and work record) and one fake node reached over the fake ssh."""

    def __init__(self, tmp_path: Path, monkeypatch, mode: str = "ok", browser: bool = True):
        self.fakes = tmp_path / "fakes"
        self.node_home = tmp_path / "node" / "home"
        self.node_tmp = tmp_path / "node" / "tmp"
        self.node_root = self.node_home / ".lina"
        for path in (self.node_home, self.node_tmp):
            path.mkdir(parents=True)
        self.ssh_log = tmp_path / "ssh.log"
        self.seen = tmp_path / "browser-seen.json"
        _script(self.fakes / "bin" / "ssh", FAKE_SSH)
        browser_cmd = _script(self.fakes / "browser", FAKE_BROWSER)
        _script(self.node_root / "bin" / "runners" / "grok", FAKE_GROK)
        _script(self.node_root / "bin" / "runners" / "agy", FAKE_AGY)
        self.cache = self.node_root / "worker" / "runners.report.json"
        self.cache.parent.mkdir(parents=True)
        self.cache.write_text('{"at": 0, "rows": []}', encoding="utf-8")
        monkeypatch.setenv("PATH", f"{self.fakes / 'bin'}{os.pathsep}{os.environ['PATH']}")
        monkeypatch.setenv("FAKE_SSH_LOG", str(self.ssh_log))
        monkeypatch.setenv("FAKE_NODE_HOME", str(self.node_home))
        monkeypatch.setenv("FAKE_NODE_TMP", str(self.node_tmp))
        monkeypatch.setenv("FAKE_BROWSER_SEEN", str(self.seen))
        monkeypatch.setattr(lina_sign_in, "MENU_DELAY_S", 0.0)
        sign_in = (f"  sign_in:\n    browser: {{command: [{browser_cmd}, {mode}]}}\n"
                   "    accounts: {grok: person@example.test}\n") if browser else ""
        self.work = WorkEnv(tmp_path, monkeypatch, extra=textwrap.dedent(f"""\
            devices:
              workers:
                {NODE}: {{host: {NODE}}}
            worker:
              runners:
                codex: {{type: command, argv: [codex, exec, "-"]}}
                grok: {{type: command, argv: [grok, -p]}}
                agy: {{type: command, argv: [agy, -p]}}
                cursor: {{type: command, argv: [cursor-agent, -p]}}
            """) + sign_in)
        self.root = self.work.root
        self.hellos = 0
        monkeypatch.setattr(lina_devices, "runner_status", self.hello)

    def hello(self, dev, worker_root) -> list[dict]:
        """What the node's hello carries: sign-in from the node's credential files, as lina_runners reads them."""
        assert dev.name == NODE and not dev.local
        self.hellos += 1

        def row(name: str, by: str, signed: bool, shim: bool = True) -> dict:
            state = {"ocx": "ocx", "cursor": "signed_out"}.get(by, "signed_in" if signed else "signed_out")
            return {"name": name, "type": "command", "pin": name, "pinned": "1.0.0", "installed": "1.0.0",
                    "sign_in": state, "sign_in_by": by, "ready": state == "signed_in" or by == "ocx",
                    "why": None, "shim": str(self.node_root / "bin" / "runners" / name) if shim else None}
        return [row("codex", "ocx", False),
                row("grok", "grok", (self.node_home / ".grok" / "auth.json").exists()),
                row("agy", "agy", (self.node_home / ".gemini/antigravity-cli/antigravity-oauth-token").exists()),
                row("cursor", "cursor", False)]

    def sign_in(self, runner: str) -> tuple[dict, str]:
        lines: list[str] = []
        lina_devices.sign_in(self.root, node=NODE, runner=runner, as_json=True, out=lines.append)
        return json.loads(lines[0]), "\n".join(lines)

    def ssh_calls(self) -> list[list[str]]:
        if not self.ssh_log.exists():
            return []
        return [json.loads(line) for line in self.ssh_log.read_text(encoding="utf-8").splitlines()]

    def inbox(self) -> list[dict]:
        return lina_board.work_read(self.root)["decide"]

    def leftovers(self) -> list[Path]:
        return sorted(self.node_tmp.glob("lina-sign-in.*"))

    def every_written_byte(self, tmp_path: Path) -> bytes:
        """Every file this run left under the scratch tree (hub, node, logs, work record), the fakes' own sources
        aside."""
        return b"\n".join(path.read_bytes() for path in sorted(tmp_path.rglob("*"))
                          if path.is_file() and self.fakes not in path.parents
                          and path.parent.name not in ("runners",))


def test_grok_signs_in_on_the_node_through_the_hub_browser_and_a_return_tunnel(tmp_path, monkeypatch):
    hub = Hub(tmp_path, monkeypatch)
    monkeypatch.setenv("GITHUB_TOKEN", "ghp_not_for_the_browser_command")

    result, _ = hub.sign_in("grok")

    assert result == {"node": NODE, "runner": "grok", "state": "signed_in",
                      "reason": "the node reports it signed in", "sign_in": "signed_in"}
    assert (hub.node_home / ".grok" / "auth.json").exists(), "the node's own CLI wrote its own credential"
    tunnels = [call for call in hub.ssh_calls() if "-L" in call]
    assert len(tunnels) == 1 and tunnels[0][tunnels[0].index("-L") + 1].count(":127.0.0.1:") == 1, \
        "the browser's return goes to the node's listener through one ssh -L"
    seen = json.loads(hub.seen.read_text(encoding="utf-8"))
    assert seen == {"argv": ["ok"], "account": "person@example.test", "return": "loopback", "got_address": True,
                    "secret": False}, \
        "the address reaches the browser command on stdin only, with the runner's account and no hub secret in its env"
    assert not hub.cache.exists(), "the node's runner report cache is gone, so hello probes again"
    assert hub.hellos == 2
    assert hub.leftovers() == [], "the node's 0600 folder with the address is removed"
    assert not _alive(int((hub.node_home / "cli.pid").read_text())), "the CLI on the node has ended"


def test_the_cli_command_signs_in_and_exits_zero(tmp_path, monkeypatch, capsys):
    from hermes_cli.subcommands.devices import build_devices_parser
    hub = Hub(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_devices, "_base", lambda base: hub.root)
    parser = argparse.ArgumentParser()
    build_devices_parser(parser.add_subparsers())
    args = parser.parse_args(["devices", "sign-in", NODE, "grok"])

    with pytest.raises(SystemExit) as ended:
        args.func(args)

    assert ended.value.code == 0
    assert capsys.readouterr().out.strip() == f"{NODE} grok: signed_in · the node reports it signed in"


def test_a_two_step_check_stops_the_flow_with_one_inbox_question_naming_node_and_runner(tmp_path, monkeypatch):
    hub = Hub(tmp_path, monkeypatch, mode="two_step")

    first, _ = hub.sign_in("grok")
    second, _ = hub.sign_in("grok")

    assert first["state"] == "needs_person" and first["need"] == "two_step"
    assert second["question"] == first["question"], "an open question for the same node and runner is kept"
    rows = hub.inbox()
    assert [row["target"] for row in rows] == [first["question"]]
    assert NODE in rows[0]["text"] and "grok" in rows[0]["text"] and "two-step" in rows[0]["text"]
    assert not (hub.node_home / ".grok" / "auth.json").exists()
    assert not _alive(int((hub.node_home / "cli.pid").read_text())), "the node's CLI is stopped"
    assert hub.leftovers() == []


@pytest.mark.parametrize("mode", ["ok", "new_device"])
def test_no_sign_in_address_code_or_token_is_printed_logged_or_left_in_a_file(tmp_path, monkeypatch, capsys, caplog,
                                                                              mode):
    hub = Hub(tmp_path, monkeypatch, mode=mode)

    result, printed = hub.sign_in("grok")
    text: list[str] = []
    lina_devices.sign_in(hub.root, node=NODE, runner="grok", out=text.append)

    assert result["state"] == ("signed_in" if mode == "ok" else "needs_person")
    shown = "\n".join([printed, *text, json.dumps(hub.inbox()), *capsys.readouterr(), caplog.text]).encode()
    left = hub.every_written_byte(tmp_path)
    for secret in (b"http", CODE.encode(), STATE.encode(), b"example.test/authorize", b"callback"):
        assert secret not in shown, secret
        assert secret not in left, secret


def test_agy_takes_the_code_its_return_page_shows_from_the_browser_command(tmp_path, monkeypatch):
    hub = Hub(tmp_path, monkeypatch, mode="code")

    result, printed = hub.sign_in("agy")

    assert result["state"] == "signed_in", result
    assert json.loads(hub.seen.read_text(encoding="utf-8"))["return"] == "code"
    assert not [call for call in hub.ssh_calls() if "-L" in call], "a return to a page needs no tunnel"
    assert CODE not in printed and hub.leftovers() == []
    assert not _alive(int((hub.node_home / "cli.pid").read_text())), "agy's terminal session is stopped"


@pytest.mark.parametrize(("runner", "state", "reason"), [
    ("codex", "signed_in", "reported only: signs in through OCX"),
    ("cursor", "failed", "reported only: signs in with a key")])
def test_runners_that_sign_in_through_ocx_or_a_key_are_reported_never_signed_in(tmp_path, monkeypatch, runner,
                                                                                 state, reason):
    hub = Hub(tmp_path, monkeypatch)

    result, _ = hub.sign_in(runner)

    assert (result["state"], result["reason"]) == (state, reason)
    assert hub.ssh_calls() == [] and not hub.seen.exists(), "nothing started on the node or in the browser"


def test_without_a_browser_command_nothing_starts_and_the_reason_says_so(tmp_path, monkeypatch):
    hub = Hub(tmp_path, monkeypatch, browser=False)

    result, _ = hub.sign_in("grok")

    assert result["state"] == "failed" and "worker.sign_in.browser.command" in result["reason"]
    assert hub.ssh_calls() == []


def test_a_second_sign_in_of_the_same_runner_on_the_same_node_does_not_start(tmp_path, monkeypatch):
    from hermes_cli.worker import settings
    hub = Hub(tmp_path, monkeypatch)
    held = lina_sign_in._lock(settings.root(hub.root), NODE, "grok")

    try:
        result, _ = hub.sign_in("grok")
    finally:
        held.close()

    assert result["state"] == "failed" and "already running" in result["reason"]
    assert hub.ssh_calls() == []
