"""Exercise compute-host rollback and crash recovery through parent JSON-RPC."""

from __future__ import annotations

import json
import os
import shutil
import signal
from collections.abc import Iterator
from pathlib import Path
from tempfile import TemporaryDirectory
from threading import Event

import pytest

from hermes_state_store import StoreUnavailableError
from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway._turn_host_parity_support import (
    MODEL_KEY,
    ROOT,
    SESSION_KEY,
    SID,
    GatewayClient,
    ModelServer,
    Reply,
    gateway,
    loopback_model,
)
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid
from tests.waiting import wait_until

pytestmark = pytest.mark.platforms("linux")
FIXTURE_SECRETS = (MODEL_KEY, APP_PASSWORD)


@pytest.fixture
def rust_binary() -> Path:
    binary = Path(shutil.which("lina-turn-host") or ROOT / "target/debug/lina-turn-host").resolve()
    assert binary.is_file(), f"Build lina-turn-host first or put it on PATH: {binary}"
    return binary


@pytest.fixture
def workdir() -> Iterator[Path]:
    output = ROOT / "out"
    output.mkdir(exist_ok=True)
    with TemporaryDirectory(dir=output, prefix="rust-host-rollback-") as path:
        yield Path(path)


@pytest.fixture
def home(workdir: Path) -> Path:
    profile = workdir / "home"
    profile.mkdir()
    return profile


@pytest.fixture
def store_dsn() -> str:
    try:
        return PROCESS_STORE.app_dsn()
    except StoreUnavailableError:
        PROCESS_STORE.refused_this_test = False
        pytest.fail("Verification E requires the disposable PostgreSQL test store with pgvector")


def _assert_model_requests(model: ModelServer, *prompts: str) -> None:
    with model.condition:
        latest_users = [
            next(message["content"] for message in reversed(request["messages"]) if message["role"] == "user")
            for request in model.requests
        ]
    assert len(latest_users) == len(prompts), latest_users
    for content, prompt in zip(latest_users, prompts, strict=True):
        # A resumed session can wrap the user text with model and date metadata.
        assert prompt in content.split("\n\n"), content


def _logged(home: Path, message: str) -> bool:
    path = home / "logs" / "errors.log"
    return path.is_file() and message in path.read_text(encoding="utf-8")


def _inline_turn(client: GatewayClient, prompt: str, answer: str, *, after: int = 0) -> None:
    result = client.rpc("prompt.submit", session_id=SID, text=prompt)
    assert result["status"] == "streaming"
    assert not result.get("turn_isolation"), result
    complete = _event(client, "message.complete", after=after)["payload"]
    assert complete["status"] == "complete", complete
    assert complete["text"] == answer


@pytest.mark.parametrize("rollback_impl", ["python", None], ids=["explicit-python", "default-python"])
def test_restart_after_config_rollback_launches_python(
    workdir: Path, home: Path, rust_binary: Path, rollback_impl: str | None, store_dsn: str,
) -> None:
    first_prompt, second_prompt = "Before rollback.", "After rollback."
    with loopback_model((Reply(text="Rust answer."), Reply(text="Python answer."))) as model:
        _config(home, workdir, model.url, "rust")
        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            assert client.rpc("session.create", cwd=str(workdir))["session_id"] == SID
            assert client.rpc("prompt.submit", session_id=SID, text=first_prompt)["turn_isolation"] is True
            rust_pid = _host_pid(home, "rust", rust_binary)
            assert _event(client, "message.complete")["payload"]["text"] == "Rust answer."
            _host_frame(client, "turn.end")
        assert not Path(f"/proc/{rust_pid}").exists()

        config_path = home / "config.yaml"
        settings = json.loads(config_path.read_text(encoding="utf-8"))
        if rollback_impl is None:
            del settings["dashboard"]["compute_host_impl"]
        else:
            settings["dashboard"]["compute_host_impl"] = rollback_impl
        config_path.write_text(json.dumps(settings), encoding="utf-8")

        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            resumed = client.rpc("session.resume", session_id=SESSION_KEY, lazy=True)
            assert resumed["session_id"] == SID
            assert resumed["session_key"] == SESSION_KEY
            assert client.rpc("prompt.submit", session_id=SID, text=second_prompt)["turn_isolation"] is True
            assert _host_pid(home, "python", rust_binary) != rust_pid
            assert _event(client, "message.complete")["payload"]["text"] == "Python answer."
            _host_frame(client, "turn.end")
        _assert_model_requests(model, first_prompt, second_prompt)
        assert any(
            message["role"] == "assistant" and message["content"] == "Rust answer."
            for message in model.requests[1]["messages"]
        )
        assert not model.errors, model.errors


def test_crash_loop_exhaustion_falls_back_only_on_next_submission(
    workdir: Path, home: Path, rust_binary: Path, store_dsn: str,
) -> None:
    crashed_prompts = ["Kill the initial Rust child.", "Kill the replacement Rust child."]
    next_prompt = "Run this new submission inline."
    gate = Event()
    replies = (
        Reply(text="Must never complete.", gate=gate),
        Reply(text="Must never complete either.", gate=gate),
        Reply(text="Inline answer."),
    )
    with loopback_model(replies) as model:
        _config(home, workdir, model.url, "rust")
        config_path = home / "config.yaml"
        settings = json.loads(config_path.read_text(encoding="utf-8"))
        settings["dashboard"]["compute_host_respawn_max"] = 1
        config_path.write_text(json.dumps(settings), encoding="utf-8")

        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            assert client.rpc("session.create", cwd=str(workdir))["session_id"] == SID
            pid = 0
            for index, prompt in enumerate(crashed_prompts):
                if index:
                    # The replacement must appear before any new prompt can implicitly start it.
                    pid = _host_pid(home, "rust", rust_binary, previous=pid)
                    _assert_model_requests(model, *crashed_prompts[:index])
                after = len(client.sink.frames())
                result = client.rpc("prompt.submit", session_id=SID, text=prompt)
                assert result.get("turn_isolation") is True, f"Crash case turn {index + 1}: {result}"
                if not index:
                    pid = _host_pid(home, "rust", rust_binary)
                model.wait_for_requests(index + 1)
                _assert_model_requests(model, *crashed_prompts[:index + 1])
                assert _host_pid(home, "rust", rust_binary) == pid
                os.kill(pid, signal.SIGKILL)
                complete = _event(client, "message.complete", after=after)["payload"]
                assert complete["status"] == "error", complete
                assert str(-signal.SIGKILL) in complete["text"], complete

            # The exit watcher sets the respawn latch after delivering the crash callback.
            wait_until(
                lambda: _logged(home, "compute host crash loop:"),
                timeout=10, what="Rust supervisor to exhaust its restart budget",
            )
            _assert_model_requests(model, *crashed_prompts)
            gate.set()
            _inline_turn(client, next_prompt, "Inline answer.", after=len(client.sink.frames()))
        _assert_model_requests(model, *crashed_prompts, next_prompt)
        completions = [
            frame["params"]["payload"]["status"]
            for frame in client.sink.frames()
            if frame.get("method") == "event"
            and frame["params"].get("session_id") == SID
            and frame["params"].get("type") == "message.complete"
        ]
        assert completions == ["error", "error", "complete"]
        assert not model.errors, model.errors


def test_missing_rust_executable_fails_open_on_first_submission(
    workdir: Path, home: Path, store_dsn: str,
) -> None:
    missing_binary = workdir / "lina-turn-host"
    child_path = os.pathsep.join((str(workdir), os.defpath))
    assert shutil.which(missing_binary.name, path=child_path) is None
    prompt = "Run with the configured Rust executable missing."
    with loopback_model((Reply(text="Startup fallback answer."),)) as model:
        _config(home, workdir, model.url, "rust")
        with gateway(home, missing_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            assert client.rpc("session.create", cwd=str(workdir))["session_id"] == SID
            _inline_turn(client, prompt, "Startup fallback answer.")
        assert _logged(home, "falling back inline")
        _assert_model_requests(model, prompt)
        assert not model.errors, model.errors
