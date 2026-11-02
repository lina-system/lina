"""scripts/agent_acceptance.py: each check passes on a working agent and fails when its surface breaks.

The agent CLI is a stand-in executable that answers like `hermes -p <agent> ...`; the gateway is a
local OpenAI-compatible endpoint. Acceptance for lina-system/lina#28.
"""

import importlib.util
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "agent_acceptance.py"
KEY = "test-gateway-key"

FAKE_CLI = r'''#!{python}
import json, sys
state = json.load(open({state!r}))
args = sys.argv[1:]
agent = args[1] if args[:1] == ["-p"] else "default"
rest = args[2:] if args[:1] == ["-p"] else args
if rest[:1] == ["chat"]:
    print(rest[rest.index("-q") + 1])
    print(state["memory"].get(agent, ""))
elif rest[:2] == ["cron", "list"]:
    print("\n".join(state["jobs"]))
elif rest[:2] == ["cron", "status"]:
    print(state["cron_status"])
elif rest[:3] == ["plugins", "list", "--json"]:
    print(json.dumps(state["plugins"]))
elif rest[:2] == ["work", "run"]:
    print("work ok for " + rest[-1])
    sys.exit(state["work_exit"])
else:
    sys.exit(2)
'''


def _load():
    spec = importlib.util.spec_from_file_location("agent_acceptance", SCRIPT)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    # dataclasses resolve the module's string annotations through sys.modules.
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


class _Gateway(BaseHTTPRequestHandler):
    paths: list[str] = []

    def log_message(self, format, *args):
        pass

    def do_POST(self):
        type(self).paths.append(self.path)
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.headers.get("Authorization") != f"Bearer {KEY}":
            self.send_response(401)
            self.end_headers()
            return
        reply = {"choices": [{"message": {"role": "assistant", "content": body["messages"][-1]["content"]}}]}
        data = json.dumps(reply).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


@pytest.fixture
def gateway():
    _Gateway.paths = []
    server = ThreadingHTTPServer(("127.0.0.1", 0), _Gateway)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{server.server_address[1]}"
    server.shutdown()
    server.server_close()
    thread.join()


@pytest.fixture
def agent_env(tmp_path, monkeypatch, gateway):
    state = {
        "memory": {"default": "staging server: acceptance-alpha; owner prefers Korean",
                   "helper": "staging server: acceptance-alpha; owner prefers Korean"},
        "jobs": ["daily-brief"], "cron_status": "✓ Gateway is running — cron jobs will fire automatically",
        "plugins": [{"name": "kanban", "status": "enabled"}, {"name": "spotify", "status": "not enabled"}],
        "work_exit": 0,
    }
    state_path = tmp_path / "state.json"
    cli = tmp_path / "agent-cli"
    cli.write_text(FAKE_CLI.format(python=sys.executable, state=str(state_path)))
    cli.chmod(0o755)
    monkeypatch.setenv("ACCEPT_KEY", KEY)

    def manifest(agents=("default",), **overrides):
        state_path.write_text(json.dumps(state))
        entry = {
            "memory": {"prompt": "Name the staging server.", "expect": ["acceptance-alpha"]},
            "person_card": {"prompt": "Which language do I want?", "expect": "korean"},
            "cron": {"jobs": ["daily-brief"]},
            "plugins": {"enabled": ["kanban"]},
            "work": {"argv": [str(cli), "work", "run", "status", "{agent}"], "expect": ["work ok for"]},
        }
        entry.update(overrides)
        doc = {"command": str(cli), "timeout": 30, "gateway": {"url": gateway, "key_env": "ACCEPT_KEY"},
               "agents": [dict(entry, name=name) for name in agents]}
        path = tmp_path / "agents.yaml"
        path.write_text(json.dumps(doc))
        return path

    return state, manifest, tmp_path


def _results(tmp_path):
    return {(r["agent"], r["check"]): r for r in json.loads((tmp_path / "out.json").read_text())}


@pytest.mark.platforms("posix")
def test_a_working_agent_passes_every_check(agent_env):
    _, manifest, tmp_path = agent_env
    code = _load().main(["--manifest", str(manifest(agents=("default", "helper"))),
                         "--json", str(tmp_path / "out.json")])
    results = _results(tmp_path)
    assert code == 0
    assert len(results) == 14 and all(r["passed"] for r in results.values())
    assert sorted(_Gateway.paths) == ["/p/helper/v1/chat/completions", "/v1/chat/completions"]


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("breakage, check", [
    (lambda s: s["memory"].update(default="owner prefers Korean"), "memory"),
    (lambda s: s.update(cron_status="⚠ Gateway is running but the cron ticker looks STALLED"), "cron"),
    (lambda s: s.update(jobs=[]), "cron"),
    (lambda s: s["plugins"][0].update(status="not enabled"), "plugins"),
    (lambda s: s.update(work_exit=3), "work"),
])
def test_a_broken_surface_fails_only_its_check(agent_env, breakage, check):
    state, manifest, tmp_path = agent_env
    breakage(state)
    code = _load().main(["--manifest", str(manifest()), "--json", str(tmp_path / "out.json")])
    failed = [key for key, r in _results(tmp_path).items() if not r["passed"]]
    assert code == 1
    assert failed == [("default", check)]


@pytest.mark.platforms("posix")
def test_a_wrong_gateway_key_fails_the_round_trip(agent_env, monkeypatch):
    _, manifest, tmp_path = agent_env
    monkeypatch.setenv("ACCEPT_KEY", "wrong")
    code = _load().main(["--manifest", str(manifest()), "--check", "gateway",
                         "--json", str(tmp_path / "out.json")])
    result = _results(tmp_path)[("default", "gateway")]
    assert code == 1 and not result["passed"] and "401" in result["detail"]


@pytest.mark.platforms("posix")
def test_an_unconfigured_check_fails_instead_of_skipping(agent_env):
    _, manifest, tmp_path = agent_env
    code = _load().main(["--manifest", str(manifest(memory={}, work={})),
                         "--check", "memory", "--check", "work", "--json", str(tmp_path / "out.json")])
    results = _results(tmp_path)
    assert code == 1
    assert [r["passed"] for r in results.values()] == [False, False]


def test_an_invalid_manifest_exits_2(tmp_path):
    path = tmp_path / "agents.yaml"
    path.write_text("agents: []\n")
    assert _load().main(["--manifest", str(path)]) == 2
