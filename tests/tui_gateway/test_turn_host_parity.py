"""Compare both real compute hosts at the model, client, and durable-store boundaries."""

from __future__ import annotations

import json
import os
import shutil
import signal
import subprocess
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import Event
from typing import Final

import pytest
from pydantic import JsonValue

from agent.context_compressor import MAX_ITERATIONS_SUMMARY_REQUEST
from hermes_state_store import StoreUnavailableError
from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway._turn_host_parity_support import (
    MODEL,
    MODEL_KEY,
    ROOT,
    SESSION_KEY,
    SID,
    UID_PREFIX,
    GatewayClient,
    Json,
    ModelServer,
    Reply,
    gateway,
    loopback_model,
    stored_rows,
)
from tests.waiting import wait_until
from tui_gateway.contracts import registry

pytestmark = pytest.mark.platforms("linux")

CASES: Final = (
    "text", "one_read", "missing_file", "offset_limit", "char_budget", "utf8_crlf",
    "two_sequential_calls", "history", "model_500", "interrupt_during_read",
    "unsupported_tool", "kill_and_restart", "turn_cap",
)
TURN_CAP: Final = 3
ANSWER: Final = "Parity model final answer."
PROMPT: Final = "Execute the parity script."
SECOND_PROMPT: Final = "Continue after the host was killed."
HISTORY: Final = [
    {"role": "user", "content": "Earlier user turn."},
    {"role": "assistant", "content": "Earlier assistant turn."},
]
# The interposer pauses a real libc read of the fixture inode. It never supplies
# bytes or changes the result of the tool, and ignores every other file descriptor.
READ_GATE_SOURCE: Final = r"""
#define _GNU_SOURCE
#include <dlfcn.h>
#include <fcntl.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <unistd.h>

ssize_t read(int fd, void *buf, size_t count) {
    ssize_t (*next_read)(int, void *, size_t) = dlsym(RTLD_NEXT, "read");
    const char *target = getenv("PARITY_READ_TARGET");
    struct stat actual, expected;
    if (target && !fstat(fd, &actual) && S_ISREG(actual.st_mode) &&
        !stat(target, &expected) && actual.st_dev == expected.st_dev &&
        actual.st_ino == expected.st_ino) {
        int marker = open(getenv("PARITY_READ_ENTERED"), O_WRONLY | O_CREAT | O_EXCL, 0600);
        if (marker >= 0) {
            char token;
            close(marker);
            int gate = open(getenv("PARITY_READ_RELEASE"), O_RDONLY);
            if (gate < 0 || next_read(gate, &token, 1) != 1)
                _exit(90);
            close(gate);
        }
    }
    return next_read(fd, buf, count);
}
"""


@dataclass(frozen=True, slots=True)
class ReadGate:
    library: Path
    entered: Path
    release: Path
    target: Path

    @property
    def env(self) -> dict[str, str]:
        return {
            "LD_PRELOAD": str(self.library),
            "PARITY_READ_ENTERED": str(self.entered),
            "PARITY_READ_RELEASE": str(self.release),
            "PARITY_READ_TARGET": str(self.target),
        }

    @contextmanager
    def arm(self) -> Iterator[None]:
        self.entered.unlink(missing_ok=True)
        # O_RDWR keeps the writer open even if the test fails before the tool enters.
        fd = os.open(self.release, os.O_RDWR | os.O_CLOEXEC)
        try:
            yield
        finally:
            os.write(fd, b"r")
            os.close(fd)


@dataclass(frozen=True, slots=True)
class Observations:
    requests: list[Json]
    events: list[Json]
    transcript: list[Json]
    host_frames: list[Json]


@pytest.fixture(scope="module")
def parity_root() -> Iterator[Path]:
    root = ROOT / "out" / "parity-tmp"
    root.mkdir(parents=True, exist_ok=True)
    with TemporaryDirectory(dir=root, prefix="turn-host-") as path:
        yield Path(path)


def _tool_call(number: int, path: str, name: str = "read_file", **pagination: int) -> Json:
    return {
        "id": f"call_parity_{number}", "type": "function",
        "function": {"name": name, "arguments": json.dumps({"path": path, **pagination})},
    }


def _script(case: str, workspace: Path) -> tuple[Reply, ...]:
    (workspace / "normal.txt").write_text("first\nsecond\nthird\nfourth\n", encoding="utf-8")
    (workspace / "second.txt").write_text("other fixture\n", encoding="utf-8")
    (workspace / "large.txt").write_text(("z" * 1000 + "\n") * 120, encoding="utf-8")
    (workspace / "unicode.txt").write_bytes("안녕하세요\r\n두 번째 줄\r\n마지막\r\n".encode())
    answer = Reply(text=ANSWER)
    calls = {
        "one_read": (_tool_call(1, "normal.txt"),),
        "missing_file": (_tool_call(1, "missing.txt"),),
        "offset_limit": (_tool_call(1, "normal.txt", offset=2, limit=2),),
        "char_budget": (_tool_call(1, "large.txt", limit=120),),
        "utf8_crlf": (_tool_call(1, "unicode.txt"),),
        "two_sequential_calls": (_tool_call(1, "normal.txt"), _tool_call(2, "second.txt")),
        "interrupt_during_read": (_tool_call(1, "normal.txt"),),
        "unsupported_tool": (_tool_call(1, "normal.txt", "parity_unknown_tool"),),
    }
    if case in calls:
        return Reply(calls=calls[case]), answer
    if case == "model_500":
        return (Reply(status=500),) * 8
    if case == "kill_and_restart":
        return Reply(text="Killed response must not be delivered.", gate=Event()), answer
    if case == "turn_cap":
        return (
            *(Reply(calls=(_tool_call(i, "normal.txt"),)) for i in range(1, TURN_CAP + 1)),
            answer,
        )
    return (answer,)


def _config(home: Path, workspace: Path, url: str, impl: str, *, max_turns: int | None = None) -> None:
    settings = {
        "dashboard": {"turn_isolation": True, "compute_host_impl": impl},
        "model": {"provider": "custom:parity", "default": MODEL, "context_length": 131072},
        "custom_providers": [{"name": "parity", "base_url": url, "api_key": MODEL_KEY}],
        "terminal": {"backend": "local", "cwd": str(workspace)},
        "agent": {"api_max_retries": 1, "auto_recovery_cycles": 0},
        "memory": {"memory_enabled": False, "user_profile_enabled": False},
        "auxiliary": {"title_generation": {"enabled": False}},
        "display": {"tool_progress": "all"},
    }
    if max_turns is not None:
        settings["agent"]["max_turns"] = max_turns
    (home / "config.yaml").write_text(json.dumps(settings), encoding="utf-8")
    # Both hosts choose spinner copy from the active skin. Use one choice so
    # the test can compare the delivered text without normalizing its payload.
    skins = home / "skins"
    skins.mkdir()
    (skins / "default.yaml").write_text(json.dumps({
        "name": "default",
        "spinner": {"thinking_faces": ["(·)"], "thinking_verbs": ["thinking"], "waiting_faces": ["(·)"]},
    }), encoding="utf-8")


def _host_pid(home: Path, impl: str, binary: Path, *, previous: int = 0) -> int:
    name = f"dashboard-rust-host-{SID}.json" if impl == "rust" else "dashboard-compute-host.json"
    registry = home / "state" / name

    def ready() -> int:
        if impl == "rust" and (registry.parent / "dashboard-compute-host.json").is_file():
            raise AssertionError("Rust arm fell back to the Python compute host")
        try:
            pid = json.loads(registry.read_text())["host_pid"]
            return pid if pid != previous and Path(f"/proc/{pid}/cmdline").is_file() else 0
        except FileNotFoundError:
            return 0

    pid = wait_until(ready, timeout=10, what=f"{impl} host registry {registry}")
    argv = Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")
    if impl == "rust":
        assert Path(f"/proc/{pid}/exe").resolve(strict=True) == binary.resolve(strict=True), argv
    else:
        assert argv[1:3] == [b"-m", b"tui_gateway.compute_host"], argv
    return pid


def _event(client: GatewayClient, kind: str, *, after: int = 0) -> Json:
    return client.wait(
        lambda frame: frame.get("method") == "event"
        and frame["params"].get("session_id") == SID and frame["params"]["type"] == kind,
        after=after,
    )["params"]


def _host_frame(client: GatewayClient, *kinds: str, after: int = 0) -> Json:
    return client.wait(
        lambda frame: frame.get("parity_host_frame", {}).get("type") in kinds,
        after=after,
    )["parity_host_frame"]


def _submit(client: GatewayClient, prompt: str) -> None:
    result = client.rpc("prompt.submit", session_id=SID, text=prompt)
    assert result.get("turn_isolation") is True, result


def _drive_turns(
    client: GatewayClient, case: str, model: ModelServer, home: Path, workspace: Path,
    impl: str, binary: Path, gate: ReadGate | None,
) -> None:
    created = client.rpc(
        "session.create", cwd=str(workspace), messages=HISTORY if case == "history" else [],
    )
    assert created["session_id"] == SID, created
    assert created["stored_session_id"] == SESSION_KEY, created
    _submit(client, PROMPT)
    pid = _host_pid(home, impl, binary)
    after = 0
    if gate is not None:
        _event(client, "tool.start")
        wait_until(gate.entered.is_file, timeout=10, what="read_file entered libc read")
        assert client.rpc("session.interrupt", session_id=SID)["status"] == "interrupted"
        assert _host_frame(client, "interrupt.ack")["applied"] is True
    if case == "kill_and_restart":
        model.wait_for_requests(1)
        os.kill(pid, signal.SIGKILL)
        killed = _event(client, "message.complete")
        assert killed["payload"]["status"] == "error", killed
        assert str(-signal.SIGKILL) in killed["payload"]["text"], killed
        after_killed = next(
            index + 1 for index, frame in enumerate(client.sink.frames())
            if frame.get("params") is killed
        )
        _event(client, "session.info", after=after_killed)
        replacement = _host_pid(home, impl, binary, previous=pid)
        after = len(client.sink.frames())
        _submit(client, SECOND_PROMPT)
        model.wait_for_requests(2)
        assert _host_pid(home, impl, binary) == replacement
        assert model.replies[0].gate is not None
        model.replies[0].gate.set()
    if gate is None:
        _host_frame(client, "turn.end", "turn.error", after=after)


def _observe(
    case: str, impl: str, root: Path, binary: Path, model: ModelServer, gate: ReadGate | None,
    store_dsn: str,
) -> Observations:
    home = root / "home"
    workspace = root / "workspace"
    home.mkdir()
    try:
        _config(home, workspace, model.url, impl, max_turns=TURN_CAP if case == "turn_cap" else None)
        env = {**gate.env, "PARITY_RUST": "1" if impl == "rust" else "0"} if gate else {}
        with gateway(home, binary, env, store_dsn, (MODEL_KEY, APP_PASSWORD)) as client:
            if gate is None:
                _drive_turns(client, case, model, home, workspace, impl, binary, gate)
            else:
                with gate.arm():
                    _drive_turns(client, case, model, home, workspace, impl, binary, gate)
                _host_frame(client, "turn.end", "turn.error")
            rows = stored_rows(home)
        frames = client.sink.frames()
        assert not any(frame.get("parity_credential_leak") for frame in frames), "Gateway emitted a fixture credential"
        serialized = json.dumps([model.requests, rows])
        assert all(secret not in serialized for secret in (MODEL_KEY, APP_PASSWORD)), (
            "A model request or transcript row contained a fixture credential"
        )
        return Observations(
            list(model.requests),
            [frame["params"] for frame in frames if frame.get("method") == "event"],
            rows,
            [frame["parity_host_frame"] for frame in frames if "parity_host_frame" in frame],
        )
    finally:
        shutil.rmtree(home)


def _normalized(value: JsonValue) -> JsonValue:
    """Preserve every key, value and list position except volatile metadata values."""
    return _normalize_value(value, {})


def _normalize_value(value: JsonValue, turn_ids: dict[str, str], parent: str = "") -> JsonValue:
    match value:
        case dict():
            normalized: Json = {}
            for key, field in value.items():
                if key == "turn_id" and isinstance(field, str) and field:
                    normalized[key] = turn_ids.setdefault(field, f"<turn {len(turn_ids) + 1}>")
                elif type(field) in (int, float) and key in {
                    "timestamp", "started_at", "ended_at", "created_at", "activity_ns",
                    "turn_started_at", "started_ns", "ended_ns", "applied_ns", "host_pid", "pid",
                }:
                    normalized[key] = "<volatile>"
                elif type(field) in (int, float) and (
                    (parent == "usage" and key in {"avg_latency_s", "avg_tps"})
                    or (parent == "payload" and "tool_id" in value and key == "duration_s")
                ):
                    # Keep the latency fields and their nullability. Their values
                    # measure elapsed wall time, including tokens divided by time.
                    normalized[key] = "<wall timing>"
                else:
                    normalized[key] = _normalize_value(field, turn_ids, key)
            return normalized
        case list():
            return [_normalize_value(item, turn_ids, parent) for item in value]
    return value


def _validate(case: str, impl: str, result: Observations) -> None:
    invalid = []
    for event in result.events:
        if event["type"] not in registry.EVENTS:
            invalid.append(f"Undeclared event type: {event['type']!r}")
        try:
            registry.check_payload(event["type"], event.get("payload"))
        except registry.ContractViolation as exc:
            invalid.append(str(exc))
    assert not invalid, "\n".join(invalid)
    assert result.requests, f"{impl} never reached the model"
    _validate_row_identity(result.transcript)
    completions = [event["payload"] for event in result.events if event["type"] == "message.complete"]
    match case:
        case "model_500":
            assert [item["status"] for item in completions] == ["error"]
        case "interrupt_during_read":
            assert [item["status"] for item in completions] == ["interrupted"]
            assert len(result.requests) == 1
        case "kill_and_restart":
            assert [item["status"] for item in completions] == ["error", "complete"]
            assert completions[-1]["text"] == ANSWER
            assert len(result.requests) == 2
        case "turn_cap":
            assert len(result.requests) == TURN_CAP + 1
            summary_messages = result.requests[-1]["messages"]
            # The same scripted answer also lets an uncapped host complete its
            # fourth ordinary request. Only the capped path sends the nudge.
            assert summary_messages[-1] == {
                "role": "user", "content": MAX_ITERATIONS_SUMMARY_REQUEST,
            }, "Expected an iteration-cap summary request after three reads"
            assert [item["tool_call_id"] for item in summary_messages if item["role"] == "tool"] == [
                f"call_parity_{i}" for i in range(1, TURN_CAP + 1)
            ]
            assert [item["status"] for item in completions] == ["complete"]
            assert completions[-1]["text"] == ANSWER
        case _:
            assert [item["status"] for item in completions] == ["complete"]
            assert completions[-1]["text"] == ANSWER
    if case not in {"model_500", "interrupt_during_read"}:
        assert result.transcript, f"{impl} did not persist any transcript rows"
        assert result.transcript[-1]["role"] == "assistant"
        assert result.transcript[-1]["content"] == ANSWER
    if case == "unsupported_tool":
        assert len(result.requests) == 2
        assert result.requests[1]["messages"][-1] == {
            "role": "tool", "tool_call_id": "call_parity_1",
            "content": "Tool 'parity_unknown_tool' does not exist. Available tools: read_file",
        }
        assert [event["type"] for event in result.events if event["type"].startswith("tool.")] == [
            "tool.generating",
        ]
        assert result.host_frames[-1]["type"] == "turn.end"
    if case == "history":
        messages = [message for message in result.requests[0]["messages"] if message["role"] != "system"]
        assert [(item["role"], item["content"]) for item in messages[:-1]] == [
            (item["role"], item["content"]) for item in HISTORY
        ]
        assert messages[-1]["role"] == "user"
        assert PROMPT in messages[-1]["content"].splitlines()
    if case in {"text", "history"}:
        assert len(result.requests) == 1
    if case == "two_sequential_calls":
        for kind in ("tool.start", "tool.complete"):
            assert [event["payload"]["tool_id"] for event in result.events if event["type"] == kind] == [
                "call_parity_1", "call_parity_2",
            ]
    _validate_reads(case, result.requests)


def _validate_row_identity(rows: list[Json]) -> None:
    minted = []
    tool_uids = {}
    for row in rows:
        minted.append(row["message_uid"])
        if stored_calls := row.get("tool_call_uids"):
            calls = json.loads(stored_calls) if isinstance(stored_calls, str) else stored_calls
            minted.extend(calls.values())
            tool_uids.update(calls)
        if row.get("role") == "tool":
            assert row["tool_call_id"] in tool_uids, "Stored tool result has no matching tool call"
            assert row["tool_call_uid"] == tool_uids[row["tool_call_id"]]
    assert all(isinstance(uid, str) and uid.startswith(UID_PREFIX) for uid in minted)
    assert len(set(minted)) == len(minted)


def _differences(python: JsonValue, rust: JsonValue, path: str = "$") -> Iterator[tuple[str, str]]:
    if type(python) is not type(rust):
        yield path, f"Python {type(python).__name__}, Rust {type(rust).__name__}"
        return
    match python, rust:
        case dict(), dict():
            if python.keys() != rust.keys():
                yield path, f"Python-only keys {python.keys() - rust.keys()}, Rust-only keys {rust.keys() - python.keys()}"
            for key in python:
                if key in rust:
                    yield from _differences(python[key], rust[key], f"{path}.{key}")
        case list(), list():
            if len(python) != len(rust):
                yield path, f"Python has {len(python)} items, Rust has {len(rust)}"
            for index, (left, right) in enumerate(zip(python, rust)):
                yield from _differences(left, right, f"{path}[{index}]")
        case _:
            if python != rust:
                yield path, f"Python {python!r:.350}, Rust {rust!r:.350}"


def _parity_differences(surface: str, python: JsonValue, rust: JsonValue) -> Iterator[str]:
    differences = _differences(python, rust)
    for path, detail in differences:
        yield f"{surface} differs at {path}: {detail}"
        if "_uid" in path:
            for other_path, other_detail in differences:
                if "_uid" not in other_path:
                    yield f"{surface} content also differs at {other_path}: {other_detail}"
                    break
        break


def _validate_reads(case: str, requests: list[Json]) -> None:
    expected_content = {
        "one_read": ["first"],
        "offset_limit": ["second"],
        "utf8_crlf": ["안녕하세요"],
        "two_sequential_calls": ["first", "other fixture"],
    }
    if case not in {*expected_content, "missing_file", "char_budget"}:
        return
    assert len(requests) == 2
    tools = [item for item in requests[1]["messages"] if item["role"] == "tool"]
    count = 2 if case == "two_sequential_calls" else 1
    assert [item["tool_call_id"] for item in tools] == [f"call_parity_{i}" for i in range(1, count + 1)]
    results = [json.loads(item["content"]) for item in tools]
    if case in expected_content:
        for marker, result in zip(expected_content[case], results, strict=True):
            assert marker in result["content"]
    if case == "missing_file":
        assert "missing.txt" in results[0]["error"]
    if case == "offset_limit":
        assert results[0]["content"].splitlines() == ["2|second", "3|third"]
    if case == "utf8_crlf":
        assert results[0]["content"] == "1|안녕하세요\r\n2|두 번째 줄\r\n3|마지막\r"
    if case == "char_budget":
        assert results[0]["truncated_by"] == "bytes"
        assert len(results[0]["content"]) <= 100_000


@pytest.mark.parametrize("case", CASES)
def test_compute_hosts_have_identical_turn_observations(
    case: str, parity_root: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(registry, "STRICT", True)
    binary = Path(shutil.which("lina-turn-host") or ROOT / "target/debug/lina-turn-host").resolve()
    if not binary.is_file():
        pytest.fail(f"Rust compute host binary does not exist: {binary}")

    root = parity_root / case
    workspace = root / "workspace"
    workspace.mkdir(parents=True)
    gate = None
    if case == "interrupt_during_read":
        gate = ReadGate(root / "gate.so", root / "read-entered", root / "release", workspace / "normal.txt")
        subprocess.run(
            ["cc", "-x", "c", "-shared", "-fPIC", "-o", str(gate.library), "-", "-ldl"],
            input=READ_GATE_SOURCE, text=True, check=True, cwd=root,
            env={"PATH": os.defpath, "TMPDIR": str(root)},
        )
        os.mkfifo(gate.release)

    failures = []
    try:
        store_dsn = PROCESS_STORE.app_dsn()
    except StoreUnavailableError:
        # The general test fixture auto-skips on this flag. B must stay red and
        # still report the observable model and client differences.
        PROCESS_STORE.refused_this_test = False
        failures.append("Disposable PostgreSQL with pgvector is unavailable; durable parity cannot pass")
        store_dsn = ""

    runs = {}
    with loopback_model(_script(case, workspace)) as model:
        for impl in ("python", "rust"):
            try:
                runs[impl] = _observe(case, impl, root, binary, model, gate, store_dsn)
            except (AssertionError, StoreUnavailableError) as exc:
                failures.append(f"{impl} observation failed: {exc}")
                PROCESS_STORE.refused_this_test = False
            failures.extend(f"{impl} model fixture: {error}" for error in model.errors)
            if impl == "python":
                PROCESS_STORE.reset()
                with model.condition:
                    model.requests.clear()
                    model.errors.clear()
                    model.replies = _script(case, workspace)

    for impl, result in runs.items():
        try:
            _validate(case, impl, result)
        except (AssertionError, ValueError) as exc:
            failures.append(f"{impl} scenario or payload assertion: {exc}")
    if set(runs) == {"python", "rust"}:
        for surface in ("requests", "events", "transcript"):
            python = _normalized(getattr(runs["python"], surface))
            rust = _normalized(getattr(runs["rust"], surface))
            failures.extend(_parity_differences(surface, python, rust))
    assert not failures, "\n".join(failures)
