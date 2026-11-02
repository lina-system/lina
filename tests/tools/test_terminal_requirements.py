import importlib
import logging



terminal_tool_module = importlib.import_module("tools.terminal_tool")


def _clear_terminal_env(monkeypatch):
    """Remove terminal env vars that could affect requirements checks."""
    keys = [
        "TERMINAL_ENV",
        "TERMINAL_CONTAINER_CPU",
        "TERMINAL_CONTAINER_DISK",
        "TERMINAL_CONTAINER_MEMORY",
        "TERMINAL_DOCKER_FORWARD_ENV",
        "TERMINAL_DOCKER_VOLUMES",
        "TERMINAL_LIFETIME_SECONDS",
        "TERMINAL_MODAL_MODE",
        "TERMINAL_SSH_HOST",
        "TERMINAL_SSH_PORT",
        "TERMINAL_SSH_USER",
        "TERMINAL_TIMEOUT",
        "TERMINAL_VERCEL_RUNTIME",
        "MODAL_TOKEN_ID",
        "MODAL_TOKEN_SECRET",
        "VERCEL_OIDC_TOKEN",
        "VERCEL_TOKEN",
        "VERCEL_PROJECT_ID",
        "VERCEL_TEAM_ID",
        "HOME",
        "USERPROFILE",
    ]
    for key in keys:
        monkeypatch.delenv(key, raising=False)
    # Default: no Nous subscription — patch both the terminal_tool local
    # binding and tool_backend_helpers (used by resolve_modal_backend_state).
    monkeypatch.setattr(terminal_tool_module, "managed_nous_tools_enabled", lambda: False)
    import tools.tool_backend_helpers as _tbh
    monkeypatch.setattr(_tbh, "managed_nous_tools_enabled", lambda: False)


def test_local_terminal_requirements(monkeypatch, caplog):
    """Local backend uses Hermes' own LocalEnvironment wrapper."""
    _clear_terminal_env(monkeypatch)
    monkeypatch.setenv("TERMINAL_ENV", "local")

    with caplog.at_level(logging.ERROR):
        ok = terminal_tool_module.check_terminal_requirements()

    assert ok is True
    assert "Terminal requirements check failed" not in caplog.text


def test_unknown_terminal_env_logs_error_and_returns_false(monkeypatch, caplog):
    _clear_terminal_env(monkeypatch)
    monkeypatch.setenv("TERMINAL_ENV", "unknown-backend")

    with caplog.at_level(logging.ERROR):
        ok = terminal_tool_module.check_terminal_requirements()

    assert ok is False
    assert any(
        "Unknown TERMINAL_ENV 'unknown-backend'" in record.getMessage()
        for record in caplog.records
    )
