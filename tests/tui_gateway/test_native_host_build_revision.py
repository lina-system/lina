"""Exercise native revision admission through a real gateway and compute host."""

from __future__ import annotations

import json
import shlex
import shutil
from pathlib import Path

import psutil
import pytest

from hermes_state_store import StoreUnavailableError
from tests._fixtures.memory_store import APP_PASSWORD, PROCESS_STORE
from tests.tui_gateway._turn_host_parity_support import MODEL_KEY, ROOT, SID, Reply, gateway, loopback_model
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_frame, _host_pid
from tui_gateway.host_supervisor import is_compute_host_identity

pytestmark = pytest.mark.platforms("linux")


@pytest.mark.parametrize("revision", [
    "matching", "packaged", "older-parent", "newer-parent", "unknown-parent",
    "missing-parent", "short-parent", "zero-parent", "non-string-parent", "missing-child", "unknown-child",
])
def test_gateway_dispatch_requires_matching_native_build(tmp_path, real_build_revision, revision):
    try:
        store_dsn = PROCESS_STORE.app_dsn()
    except StoreUnavailableError:
        PROCESS_STORE.refused_this_test = False
        pytest.fail("Revision fallback requires the disposable PostgreSQL test store with pgvector")
    binary = Path(shutil.which("lina-turn-host") or ROOT / "target/debug/lina-turn-host").resolve()
    assert binary.is_file(), f"Build lina-turn-host first or put it on PATH: {binary}"
    home = tmp_path / "home"
    home.mkdir()
    env = {}
    stamps = {
        "packaged": real_build_revision,
        "older-parent": "a" * 40,
        "newer-parent": "b" * 40,
        "unknown-parent": "unknown",
        "short-parent": real_build_revision[:7],
        "zero-parent": "0" * 40,
        "non-string-parent": 123,
    }
    if revision in stamps:
        # This is the canonical lookup path for an install with an external stamp.
        install = tmp_path / "install"
        install.mkdir()
        (install / "install-stamp.json").write_text(json.dumps({
            "commit": stamps[revision],
            "updateMechanism": "external",
        }), encoding="utf-8")
        env["HERMES_INSTALL_ROOT"] = str(install)
    if revision in {"missing-parent", "zero-parent"}:
        # This install has no checkout identity to fall back to.
        env["GIT_DIR"] = str(tmp_path / "missing-git")
    attempted_pid = tmp_path / "attempted-pid"
    if revision in {"missing-child", "unknown-child"}:
        # Old or unstamped child implementations can send either hello variant.
        binary = tmp_path / "lina-turn-host"
        hello = {"type": "hello", "hermes_home": str(home)}
        if revision == "unknown-child":
            hello["build_sha"] = "unknown"
        wire = shlex.quote(json.dumps(hello))
        binary.write_text(
            f"#!/bin/sh\nprintf '%s\\n' \"$$\" > {shlex.quote(str(attempted_pid))}\n"
            f"printf '%s\\n' {wire}\nwhile read -r line; do :; done\n",
            encoding="utf-8",
        )
        binary.chmod(0o755)

    answer = "Revision check complete."
    with loopback_model((Reply(text=answer),)) as model:
        _config(home, tmp_path, model.url, "rust")
        with gateway(home, binary, env, store_dsn, (MODEL_KEY, APP_PASSWORD)) as client:
            assert client.rpc("session.create", cwd=str(tmp_path))["session_id"] == SID
            result = client.rpc("prompt.submit", session_id=SID, text="Check the selected host.")
            assert result.get("turn_isolation") is True, result
            impl = "rust" if revision in {"matching", "packaged"} else "python"
            if impl == "python":
                assert not (home / "state" / f"dashboard-rust-host-{SID}.json").exists()
            pid = _host_pid(home, impl, binary)
            assert [
                child.pid for child in psutil.Process(client.process.pid).children()
                if is_compute_host_identity(child.pid)
            ] == [pid]
            if revision in {"missing-child", "unknown-child"}:
                assert not Path(f"/proc/{attempted_pid.read_text().strip()}").exists()
            assert _event(client, "message.complete")["payload"]["text"] == answer
            _host_frame(client, "turn.end")
        assert not Path(f"/proc/{pid}").exists()
        assert len(model.requests) == 1
        assert model.errors == []
