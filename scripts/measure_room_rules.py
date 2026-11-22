"""Capture a synthetic Hermes turn and replay it three times against the local Magpie pool.

Run with the checkout on PYTHONPATH and its PM test interpreter. Use a fresh HOME
and TMPDIR. Capture starts its own disposable PostgreSQL, never an existing DSN.
`capture OUTPUT` creates the five fixtures. `send OUTPUT CASE` sends the captured
request, retaining each exact body, HTTP status and response under OUTPUT's parent.
Use sibling OUTPUT directories for the baseline and patched captures.
"""

from __future__ import annotations

import argparse
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import tempfile
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener
from uuid import uuid4

MODEL = "hub/anthropic/claude-haiku-5-5"
URL = "http://127.0.0.1:3425/v1/chat/completions"
QUESTION = "비 오는 날 외출할 때 챙길 물건 하나를 한 문장으로 추천해 줘."
POLITE = "항상 존댓말로 답해."
PLAIN = "studio 방에서는 반말로 답해."
CASES = {
    "empty": ("studio", ()),
    "global-only": ("studio", (POLITE,)),
    "room-only": ("studio", (PLAIN,)),
    "conflict": ("studio", (POLITE, PLAIN)),
    "outside": ("garden", (POLITE, PLAIN)),
}


def seed(store, statements):
    import psycopg

    with psycopg.connect(store.superuser_dsn()) as owner, psycopg.connect(store.app_dsn()) as app:
        app.execute("SELECT set_config('lina.agent_id', 'default', false)")
        for index, text in enumerate(statements):
            uid = f"agent:default:rule-fixture-{index}"
            owner.execute(
                """INSERT INTO memory.messages
                   (uid,source,agent_id,session_id,role,content,content_sha256,speaker,ts)
                   VALUES (%s,'agent','default','fixture-author','user',%s,%s,'owner','2026-01-02')""",
                (uid, text, hashlib.sha256(text.encode()).hexdigest()),
            )
            owner.commit()
            scope, target, place = ("place", "agent:default", "channel:studio") if text == PLAIN else ("all", "*", None)
            app.execute(
                "SELECT memory.record_standing_rule(%s,%s,'conduct',%s,%s,%s,%s)",
                (uuid4(), uid, scope, target, place, text),
            )


def capture(output: Path) -> None:
    if any(os.environ.get(key) for key in ("LINA_STORE_DSN", "LINA_TEST_PG_DSN", "LINA_HOME", "HERMES_HOME")):
        raise SystemExit("Capture requires a fresh process without a configured agent home or store.")
    from contextlib import chdir
    with tempfile.TemporaryDirectory() as temp, chdir(temp):
        home = Path(temp) / "profile"
        home.mkdir()
        with patch.dict(os.environ, {"HOME": temp, "HERMES_HOME": str(home), "LINA_HOME": str(home)}):
            capture_cases(output, home)


def capture_cases(output: Path, home: Path) -> None:
    from datetime import UTC, datetime
    from agent.title_generator import _auto_title_enabled
    from hermes_state import SessionDB
    from run_agent import AIAgent
    from tests._fixtures.memory_store import ProcessStore
    from tests.plugins.memory.lina import cache_server

    home.joinpath("config.yaml").write_text(
        "auxiliary:\n  title_generation:\n    enabled: false\n"
        "model:\n  context_length: 200000\n", encoding="utf-8",
    )
    if _auto_title_enabled():
        raise RuntimeError("Title generation is still enabled")
    clock = lambda: datetime(2026, 1, 3, 12, tzinfo=UTC)
    output.mkdir(parents=True, exist_ok=False)
    with closing(ProcessStore()) as store:
        for case, (room, statements) in CASES.items():
            store.reset()
            seed(store, statements)
            captured = []
            wire = cache_server.wire

            def record(body):
                captured.append(body)
                return wire(body)

            with (
                cache_server.cache_model(clock, "room-check") as server,
                SessionDB(home / "state.db") as db,
                patch.object(cache_server, "wire", record),
                patch("agent.prompt_builder.build_environment_hints", return_value=""),
                patch("agent.system_prompt.get_default_hermes_root", return_value=Path("/fixture")),
                patch("tools.env_probe.get_environment_probe_line", return_value=""),
                patch("hermes_time.now", clock),
            ):
                agent = AIAgent(
                    provider="magpie", model=MODEL, base_url=server.url, api_key="loopback",
                    session_id=f"room-measure-{case}", session_db=db, gateway_session_key=room,
                    quiet_mode=True, skip_memory=True, skip_context_files=True,
                    skip_background_review=True, enabled_toolsets=[], platform="cli",
                )
                agent.session_start = clock()
                result = agent.run_conversation(QUESTION)
                if len(captured) != 1 or result.get("final_response") != "reply 1: noted.":
                    raise RuntimeError(f"{case}: expected exactly one captured fixture request, got {len(captured)}")
                body = captured[0]
                section = next((s.content for s in agent._plugin_system_prompt_sections_snapshot
                                if s.id == "owner-standing-rules"), "")
            expected = {text for text in statements if text == POLITE or room == "studio"}
            if {text for text in (PLAIN, POLITE) if text in section} != expected:
                raise RuntimeError(f"{case}: wrong rule selection: {section!r}")
            # Replay one ordinary, non-streamed completion; messages and tools are the captured wire data.
            body["stream"] = False
            body.pop("stream_options", None)
            if body["model"] != MODEL or QUESTION not in str(body["messages"][-1]):
                raise RuntimeError(f"{case}: wrong model or final user message")
            (output / f"{case}.json").write_text(json.dumps(body, ensure_ascii=False), encoding="utf-8")
            (output / f"{case}.section").write_bytes(section.encode())
            print(case, len(section.encode()), hashlib.sha256(section.encode()).hexdigest(), flush=True)


def send(output: Path, case: str, location_probe: bool) -> None:
    body = (output / f"{case}.json").read_bytes()
    if location_probe:
        data = json.loads(body)
        room, _ = CASES[case]
        data["messages"][-1]["content"] += f"\n\n[System: Current place for this turn: channel:{room}]"
        body = json.dumps(data, ensure_ascii=False).encode()
        case += "-location-probe"
    if json.loads(body)["model"] != MODEL:
        raise SystemExit("Unexpected model in capture")
    attempts = output.parent / "attempts"
    attempts.mkdir(exist_ok=True)
    opener = build_opener(ProxyHandler({}))
    for repeat in range(1, 4):
        trial = output / f"{case}-{repeat}"
        trial.mkdir()
        for slot in range(1, 31):
            try:
                (attempts / str(slot)).mkdir()
                break
            except FileExistsError:
                continue
        else:
            raise SystemExit("30 inference requests already reserved")
        (trial / "request.json").write_bytes(body)
        request = Request(URL, body, {"Content-Type": "application/json"}, method="POST")
        try:
            response = opener.open(request, timeout=120)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read()
            (trial / "response.json").write_bytes(raw)
            (trial / "status.txt").write_text(f"HTTP {response.status}\n", encoding="ascii")
            print(trial.name, response.status, flush=True)
            choices = json.loads(raw).get("choices") if response.status == 200 else None
            if (not choices or choices[0].get("finish_reason") == "content_filter"
                    or choices[0]["message"].get("refusal") or not choices[0]["message"].get("content")):
                raise SystemExit(f"Magpie refused the request; see {trial / 'response.json'}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    for action in ("capture", "send"):
        command = commands.add_parser(action)
        command.add_argument("output", type=Path)
        if action == "send":
            command.add_argument("case", choices=CASES)
            command.add_argument("--location-probe", action="store_true",
                                 help="Change only the current user row for a controlled location comparison.")
    args = parser.parse_args()
    if args.action == "capture":
        capture(args.output.resolve())
    else:
        send(args.output.resolve(), args.case, args.location_probe)
