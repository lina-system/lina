"""Explicit direct mode cannot fall back to the managed gateway."""
import pytest

def test_terminal_tool_respects_direct_modal_mode_without_falling_back_to_managed(monkeypatch):
    from tools.environments import modal

    monkeypatch.setattr(modal, "has_direct_modal_credentials", lambda: False)
    monkeypatch.setattr(modal, "is_managed_tool_gateway_ready", lambda _vendor: True)
    with pytest.raises(ValueError, match="direct Modal credentials"):
        modal._build_modal_env(image="python:3.11", cwd="/root", timeout=60,
                               cc={"modal_mode": "direct"}, task_id="task-modal-direct-only")
