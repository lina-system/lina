"""``lina cron ops``: the ops launchd agents and timers become script-only cron jobs of the profile (lina#38,
lina#484)."""

import datetime as dt
import json
import runpy
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from hermes_constants import get_default_hermes_root, get_hermes_home


def _jobs() -> dict:
    from cron.jobs import list_jobs
    return {j["name"]: j for j in list_jobs(include_disabled=True)}


def _run_script(name: str, *args: str) -> int:
    path = get_hermes_home() / "scripts" / name
    saved = sys.argv
    sys.argv = [str(path), *args]
    try:
        runpy.run_path(str(path), run_name="__main__")
    except SystemExit as exc:
        return int(exc.code or 0)
    finally:
        sys.argv = saved
    return 0


def test_install_creates_script_jobs_that_run_the_lina_commands(monkeypatch):
    # Given
    from hermes_cli import cron_ops
    seen = []
    import hermes_cli.main as cli_main
    monkeypatch.setattr(cli_main, "main", lambda: seen.append(list(sys.argv)))
    monkeypatch.setattr(cron_ops, "start_reviews", lambda dry_run=False: seen.append(["review", dry_run]) or 0)
    # When
    result = cron_ops.install()
    # Then
    assert result == [("learned-collect", "created"), ("learned-card", "created"), ("agent-review", "created"),
                      ("undo-snapshot", "created")]
    jobs = _jobs()
    assert {name: (j["schedule"]["expr"], j["no_agent"], j["deliver"]) for name, j in jobs.items()} == {
        "learned-collect": ("5 * * * *", True, "local"),
        "learned-card": ("5 11 * * *", True, "local"),
        "agent-review": ("40 23 * * *", True, "local"),
        "undo-snapshot": ("50 23 * * *", True, "local")}
    for job in jobs.values():
        assert _run_script(job["script"]) == 0
    assert _run_script(jobs["undo-snapshot"]["script"], "--dry-run") == 0
    assert _run_script(jobs["agent-review"]["script"], "--dry-run") == 0
    assert seen == [["lina", "learned", "collect"], ["lina", "learned", "card", "--day", "yesterday", "--send"],
                    ["review", False], ["lina", "undo", "snapshot"], ["lina", "undo", "snapshot", "--dry-run"],
                    ["review", True]]


def test_install_again_updates_in_place_and_remove_deletes_jobs_and_scripts():
    # Given
    from hermes_cli import cron_ops
    cron_ops.install()
    ids = {name: j["id"] for name, j in _jobs().items()}
    # When
    again = cron_ops.install()
    # Then
    assert [what for _, what in again] == ["updated"] * 4
    assert {name: j["id"] for name, j in _jobs().items()} == ids
    # When
    removed = cron_ops.remove()
    # Then
    assert removed == ["learned-collect", "learned-card", "agent-review", "undo-snapshot"]
    assert _jobs() == {}
    assert not list((get_hermes_home() / "scripts").glob("lina-*.py"))


class _Gateway(BaseHTTPRequestHandler):
    """A loopback Runs API that, like the real one, replays a run for a reused Idempotency-Key."""

    runs: dict = {}
    requests: list = []

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        key = self.headers["Idempotency-Key"]
        self.requests.append((self.path, self.headers["Authorization"], key, body["input"]))
        replayed = key in self.runs
        self.runs.setdefault(key, f"run-{len(self.runs) + 1}")
        data = json.dumps({"run_id": self.runs[key], "status": "started", "replayed": replayed}).encode()
        self.send_response(202)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


def _token(name: str) -> str:
    return f"{name}-profile-gateway-token"


def _profile(name: str, *, heart: str = ""):
    home = get_default_hermes_root() / "profiles" / name
    home.mkdir(parents=True)
    (home / "profile.yaml").write_text("name: " + name)
    if heart == "journal":
        (home / "heart").mkdir()
    elif heart == "note":
        (home / "SOUL.self.md").write_text("synthetic self note\n")
    (home / ".env").write_text(f"API_SERVER_KEY={_token(name)}\n")


@pytest.fixture
def gateway(monkeypatch):
    monkeypatch.setenv("NO_PROXY", "127.0.0.1")
    monkeypatch.setattr(_Gateway, "runs", {})
    monkeypatch.setattr(_Gateway, "requests", [])
    server = ThreadingHTTPServer(("127.0.0.1", 0), _Gateway)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    root = get_default_hermes_root()
    root.mkdir(parents=True, exist_ok=True)
    (root / ".env").write_text(f"API_SERVER_PORT={server.server_address[1]}\n")
    _profile("alpha", heart="journal")
    _profile("beta", heart="note")
    _profile("gamma")
    yield _Gateway
    server.shutdown()
    server.server_close()


def test_review_starts_one_turn_per_heart_agent_and_a_second_start_replays(gateway, capsys):
    from hermes_cli import cron_ops
    day = dt.date.today().isoformat()
    # When the job starts the day's reviews twice.
    first = cron_ops.start_reviews()
    second = cron_ops.start_reviews()
    # Then only the heart-keeping agents got a turn, each through its own gateway mirror and key.
    assert (first, second) == (0, 0)
    sent = {(path, auth, key) for path, auth, key, _ in gateway.requests}
    assert sent == {("/p/alpha/v1/runs", f"Bearer {_token('alpha')}", f"lina-daily-review-alpha-{day}"),
                    ("/p/beta/v1/runs", f"Bearer {_token('beta')}", f"lina-daily-review-beta-{day}")}
    assert all(f"lina heart review-input --date {day}" in text for *_, text in gateway.requests)
    # And the second start reused the day's key, so each agent has one run.
    assert len(gateway.runs) == 2
    assert capsys.readouterr().out.count("(already started today)") == 2


def test_review_dry_run_sends_nothing_and_a_missing_key_fails_only_that_agent(gateway, capsys):
    from hermes_cli import cron_ops
    # When running dry.
    assert cron_ops.start_reviews(dry_run=True) == 0
    # Then it names each turn it would start and sends nothing.
    out = capsys.readouterr().out
    assert "would start alpha: POST http://127.0.0.1:" in out and "/p/beta/v1/runs" in out
    assert gateway.requests == []
    # When one agent has no gateway key.
    (get_default_hermes_root() / "profiles" / "beta" / ".env").unlink()
    code = cron_ops.start_reviews()
    # Then the others still start and the job reports the failure.
    assert code == 1
    assert [path for path, *_ in gateway.requests] == ["/p/alpha/v1/runs"]
    assert "beta: no API_SERVER_KEY" in capsys.readouterr().err
