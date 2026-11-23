"""Drive the headless dashboard through its public JSON-RPC WebSocket."""

import argparse
import json
import os
import re
import secrets
import subprocess
import sys
import tempfile
from contextlib import contextmanager
from pathlib import Path
from time import monotonic, monotonic_ns, sleep
from typing import Final, Iterator

import anyio
from websockets.asyncio.client import ClientConnection, connect
from websockets.exceptions import WebSocketException

from host_metrics import MeasurementError, Process, TICKS, registered_host
from model_fixture import MODEL, Fixture, ModelTurn, loopback_model

ROOT: Final = Path(__file__).resolve().parents[1]
TIMEOUT: Final = 90
TRACKED_EVENTS: Final = ("tool.start", "tool.complete", "message.complete")


def prepare(scratch: Path, impl: str, fixture: Fixture, token: str) -> tuple[Path, dict[str, str]]:
    home = scratch / "state"
    account = scratch / "account"
    for path in (home, account, scratch / "tmp", scratch / "runtime"):
        path.mkdir(mode=0o700)
    base_url = f"http://127.0.0.1:{fixture.server_port}/v1"
    (home / "config.yaml").write_text(json.dumps({
        "model": {
            "provider": "custom", "default": MODEL, "base_url": base_url, "context_length": 256000,
        },
        "auxiliary": {"title_generation": {"enabled": False}},
        "dashboard": {
            "turn_isolation": impl != "inproc",
            "compute_host_impl": "rust" if impl == "rust" else "python",
        },
    }))
    (home / "models_dev_cache.json").write_text(json.dumps({
        "fixture-offline": {"id": "fixture-offline", "env": [], "models": {}},
    }))
    env = {key: os.environ[key] for key in ("PATH", "LANG", "TZ") if key in os.environ}
    env.update(
        HOME=str(account), XDG_CONFIG_HOME=str(account / ".config"),
        XDG_CACHE_HOME=str(account / ".cache"), XDG_DATA_HOME=str(account / ".local" / "share"),
        XDG_STATE_HOME=str(account / ".local" / "state"),
        XDG_RUNTIME_DIR=str(scratch / "runtime"), TMPDIR=str(scratch / "tmp"),
        HERMES_HOME=str(home), HERMES_RUNTIME_DIR=str(scratch / "runtime"),
        HERMES_DASHBOARD_SESSION_TOKEN=token, HERMES_TUI_TOOLSETS="file_read",
        OPENAI_API_KEY="fixture-only", OPENAI_BASE_URL=base_url,
        PYTHONPATH=str(ROOT), PYTHONDONTWRITEBYTECODE="1", PYTHONUNBUFFERED="1",
    )
    if dsn := os.environ.get("MEASURE_STORE_DSN"):
        from psycopg.conninfo import conninfo_to_dict

        options = conninfo_to_dict(dsn)
        hosts = [options[key] for key in ("host", "hostaddr") if key in options]
        if not hosts or set(hosts) - {"127.0.0.1", "::1"} or "service" in options:
            raise MeasurementError("MEASURE_STORE_DSN must name an isolated loopback PostgreSQL store")
        env["LINA_STORE_DSN"] = dsn
    return home, env


@contextmanager
def dashboard(scratch: Path, env: dict[str, str]) -> Iterator[tuple[int, int]]:
    log_path = scratch / "dashboard.log"
    print(f"Dashboard log {log_path.relative_to(ROOT)}", file=sys.stderr)
    with log_path.open("w") as log, subprocess.Popen(
        [sys.executable, "-B", "-m", "hermes_cli.main", "serve",
         "--isolated", "--host", "127.0.0.1", "--port", "0"],
        cwd=ROOT, env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
    ) as child:
        try:
            deadline = monotonic() + TIMEOUT
            with log_path.open() as output:
                while child.poll() is None and monotonic() < deadline:
                    line = output.readline()
                    ready = re.fullmatch(r"HERMES_BACKEND_READY port=(\d+)\n", line)
                    if ready:
                        yield child.pid, int(ready[1])
                        return
                    if not line:
                        sleep(0.05)
            raise MeasurementError(f"dashboard did not become ready (exit={child.poll()}), see {log_path}")
        finally:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait()


async def send(ws: ClientConnection, method: str, request_id: str, **params: str) -> None:
    await ws.send(json.dumps({
        "jsonrpc": "2.0", "id": request_id, "method": method, "params": params,
    }))


async def create_session(ws: ClientConnection, cwd: Path) -> str:
    with anyio.fail_after(TIMEOUT):
        ready = json.loads(await ws.recv())["params"]
        if ready["type"] != "gateway.ready" or ready["payload"].get("session_authority"):
            raise MeasurementError("expected standalone dashboard gateway.ready")
        await send(ws, "session.create", "create", source="tui", cwd=str(cwd))
        while True:
            frame = json.loads(await ws.recv())
            if frame.get("id") == "create":
                if "error" in frame:
                    raise MeasurementError(f"session.create refused: {frame['error']}")
                return frame["result"]["session_id"]


async def collect_events(ws: ClientConnection, turn: ModelTurn) -> tuple[list[dict], int]:
    """Collect events until the admission RPC and the post-completion idle frame."""
    events = []
    reply = False
    idle_ns = 0
    with anyio.fail_after(TIMEOUT):
        while not (reply and idle_ns):
            raw = await ws.recv()
            observed_ns = monotonic_ns()
            frame = json.loads(raw)
            if frame.get("id") == turn.call_id:
                if "error" in frame:
                    raise MeasurementError(f"prompt.submit refused: {frame['error']}")
                reply = True
            if frame.get("method") != "event":
                continue
            event = frame["params"]
            if event.get("session_id") != turn.sid:
                continue
            kind = event["type"]
            if kind in TRACKED_EVENTS:
                events.append({"type": kind, "received_ns": observed_ns, "payload": event["payload"]})
                if kind == "message.complete" and (
                    event["payload"].get("text") != turn.response
                    or event["payload"].get("status") != "complete"
                ):
                    raise MeasurementError(f"unexpected completion: {event['payload']}")
            if (events and events[-1]["type"] == "message.complete" and kind == "session.info"
                    and event["payload"].get("running") is False):
                idle_ns = observed_ns
    return events, idle_ns


async def measure_turn(
    ws: ClientConnection, fixture: Fixture, turn: ModelTurn, session: int, number: int,
) -> dict:
    parent_before = Process.read(turn.parent_pid)
    host_before = registered_host(turn.home, turn.sid, turn.impl)
    cold = None
    if number == 1:
        cold = {"parent": parent_before.pss_kib(), "host": host_before.pss_kib() if host_before else None}
    fixture.turn = turn
    sent_ns = monotonic_ns()
    await send(ws, "prompt.submit", turn.call_id, session_id=turn.sid, text=turn.prompt)
    events, idle_ns = await collect_events(ws, turn)
    parent_after = Process.read(turn.parent_pid)
    host_after = registered_host(turn.home, turn.sid, turn.impl)
    if [event["type"] for event in events] != list(TRACKED_EVENTS):
        raise MeasurementError(f"expected one start, tool completion and message completion, got {events}")
    if any(event["payload"].get("name") != "read_file"
           or event["payload"].get("tool_id") != turn.call_id for event in events[:2]):
        raise MeasurementError(f"tool events do not match the read_file call: {events[:2]}")
    if len(turn.requests) != 2 or fixture.errors:
        raise MeasurementError(f"expected two successful model requests, observed {len(turn.requests)}")
    if turn.impl != "inproc" and host_after is None:
        raise MeasurementError(f"{turn.impl} host vanished before CPU sampling")
    owner = host_after or parent_after
    if any((request["owner"]["pid"], request["owner"]["start_ticks"])
           != (owner.pid, owner.start_ticks) for request in turn.requests):
        raise MeasurementError("model socket owner changed during the turn")
    start, tool_end, complete = [event["received_ns"] for event in events]
    fixture_ns = sum(request["sent_ns"] - request["received_ns"] for request in turn.requests)
    row = {
        "schema": 1, "impl": turn.impl, "session": session, "session_id": turn.sid, "turn": number,
        "parent": parent_after.identity(), "host": host_after.identity() if host_after else None,
        "cpu_clock_ticks_per_s": TICKS, "submit_ns": sent_ns, "idle_observed_ns": idle_ns,
        "marker": turn.marker, "model_requests": turn.requests, "events": events,
        "elapsed_ms": (complete - sent_ns) / 1e6,
        "fixture_service_ms": fixture_ns / 1e6,
        "model_excluded_ms": (complete - sent_ns - fixture_ns) / 1e6,
        "submit_to_tool_start_ms": (start - sent_ns) / 1e6,
        "tool_start_to_complete_ms": (tool_end - start) / 1e6,
        "tool_complete_to_message_complete_ms": (complete - tool_end) / 1e6,
        "parent_cpu": parent_after.cpu_ms(parent_before),
        "host_cpu": host_after.cpu_ms(host_before) if host_after else None,
        "cold_pss_kib": cold,
        "cold_live_pss_kib": turn.requests[0]["cold_live_pss_kib"],
        "after_first_pss_kib": (
            {"parent": parent_after.pss_kib(), "host": host_after.pss_kib() if host_after else None}
            if number == 1 else None
        ),
        "after_10_pss_kib": (
            {"parent": parent_after.pss_kib(), "host": host_after.pss_kib() if host_after else None}
            if number == 10 else None
        ),
    }
    fixture.turn = None
    return row


async def run_session(
    port: int, token: str, pid: int, home: Path, fixture: Fixture,
    impl: str, session: int, turns: int,
) -> list[dict]:
    rows = []
    workspace = home.parent / "workspace"
    workspace.mkdir()
    async with connect(f"ws://127.0.0.1:{port}/api/ws?token={token}", proxy=None,
                       compression=None, max_size=8 * 1024 * 1024) as ws:
        sid = await create_session(ws, workspace)
        for number in range(1, turns + 1):
            marker = f"loopback-file-{secrets.token_hex(12)}"
            path = workspace / f"turn-{number}.txt"
            path.write_text(marker + "\n")
            call_id = f"call_{session}_{number}"
            turn = ModelTurn(f"Read the fixture for turn {session}/{number}.", marker, path,
                             home, sid, impl, pid, call_id, sample_cold_pss=number == 1)
            rows.append(await measure_turn(ws, fixture, turn, session, number))
        # Catch trailing tracked events while there is still an open socket.
        with anyio.move_on_after(0.1):
            while True:
                event = json.loads(await ws.recv()).get("params", {})
                if event.get("session_id") == sid and event.get("type") in TRACKED_EVENTS:
                    raise MeasurementError(f"extra turn event after settlement: {event['type']}")
    return rows


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--impl", choices=("inproc", "python", "rust"), required=True)
    parser.add_argument("--sessions", type=int, default=5)
    parser.add_argument("--turns", type=int, default=10)
    args = parser.parse_args()
    if args.sessions < 1 or args.turns < 1:
        parser.error("sessions and turns must be positive")
    scratch_root = ROOT / "out" / "measure-tmp"
    scratch_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    run_root = Path(tempfile.mkdtemp(prefix=f"{args.impl}-", dir=scratch_root))
    rows = []
    with loopback_model() as fixture:
        try:
            for session in range(1, args.sessions + 1):
                token = secrets.token_urlsafe(32)
                scratch = run_root / str(session)
                scratch.mkdir(mode=0o700)
                home, env = prepare(scratch, args.impl, fixture, token)
                with dashboard(scratch, env) as (pid, port):
                    rows.extend(anyio.run(run_session, port, token, pid, home, fixture,
                                          args.impl, session, args.turns))
                print(f"{args.impl} session {session}/{args.sessions} verified", file=sys.stderr)
            if fixture.errors:
                raise MeasurementError("unexpected requests reached the fixture")
        except (MeasurementError, TimeoutError, OSError, WebSocketException) as exc:
            detail = f"; model fixture: {fixture.errors}" if fixture.errors else ""
            if fixture.turn is not None:
                failure = run_root / "failed-turn.json"
                failure.write_text(json.dumps({
                    "impl": args.impl, "session_id": fixture.turn.sid, "marker": fixture.turn.marker,
                    "model_requests": fixture.turn.requests, "fixture_errors": fixture.errors,
                }, indent=2) + "\n")
                detail += f"; partial wire evidence {failure.relative_to(ROOT)}"
            parser.exit(1, f"{args.impl} measurement failed: {exc}{detail}\n")
    output = ROOT / "out" / f"turn-{args.impl}.jsonl"
    pending = run_root / output.name
    pending.write_text("".join(json.dumps(row) + "\n" for row in rows))
    pending.replace(output)
    print(f"{output.relative_to(ROOT)} ({len(rows)} verified turns)")


if __name__ == "__main__":
    main()
