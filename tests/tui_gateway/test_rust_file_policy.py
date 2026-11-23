import json
import os
from pathlib import Path

import pytest

from agent.redact import clear_vault_redaction_values, register_vault_redaction_value
from hermes_constants import get_hermes_home
from tools.file_tools import read_file_tool
from tui_gateway.rust_file_policy import handle_file_policy


def exchange(workspace: Path, op: str, path: str, **fields) -> dict:
    return handle_file_policy({"cwd": str(workspace)}, {
        "type": "read_file.policy", "sid": "session", "turn_id": "turn",
        "request_id": "request", "op": op, "path": path, **fields,
    })


def test_protected_target_can_resolve_into_workspace(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    public_name = workspace / "ordinary.txt"
    public_name.write_text("private synthetic fixture")
    credential_path = get_hermes_home() / "auth.json"
    credential_path.symlink_to(public_name)
    reply = exchange(workspace, "authorize", public_name.name)
    assert reply["ok"] is False
    assert "credential store" in reply["error"]
    assert "private synthetic fixture" not in str(reply)


def test_redaction_uses_live_registry_and_rechecks_source_identity(tmp_path):
    path = tmp_path / "notes"
    marker = "synthetic-policy-canary"
    path.write_text(marker)
    grant = exchange(tmp_path, "authorize", path.name)
    assert grant["ok"] is True
    request = {"identity": grant["identity"], "resolved": grant["resolved"], "content": f"1|{marker}"}
    register_vault_redaction_value(marker)
    try:
        ack = exchange(tmp_path, "redact", path.name, **request)
        assert ack["content"] == "1|«redacted-vault-secret»"
        replacement = tmp_path / "replacement"
        replacement.write_text("different synthetic content")
        replacement.replace(path)
        denied = exchange(tmp_path, "redact", path.name, **request)
        assert denied["ok"] is False
        assert marker not in str(denied)
    finally:
        clear_vault_redaction_values()


def test_control_frame_accepts_neither_child_cwd_nor_tool_dispatch(tmp_path):
    (tmp_path / "file").write_text("local text")
    assert exchange(tmp_path, "authorize", "file", cwd=str(tmp_path))["ok"] is False
    assert exchange(tmp_path, "execute", "file", tool="read_file")["ok"] is False
    assert exchange(tmp_path, "authorize", r"\??\example")["ok"] is False


@pytest.mark.platforms("linux")
def test_fifo_authorization_matches_python_refusal(tmp_path):
    fifo = tmp_path / "é😀"
    os.mkfifo(fifo)
    reply = exchange(tmp_path, "authorize", str(fifo))
    assert reply["ok"] is False
    assert "FIFO (named pipe)" in reply["result"]["note"]
    assert json.dumps(reply["result"]) == read_file_tool(str(fifo))
