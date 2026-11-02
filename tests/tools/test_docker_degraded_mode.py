"""Docker connection errors and degraded tool results."""
import json
import subprocess
import pytest
from tools.environments.base import EnvironmentConnectionError
pytest_plugins = ["tests.tools.test_terminal_degraded_mode"]

def test_docker_daemon_timeout_raises_connection_error(monkeypatch):
    from tools.environments import docker as docker_env

    monkeypatch.setattr(docker_env, "find_docker", lambda: "/usr/bin/docker")

    def _timeout(*a, **k):
        raise subprocess.TimeoutExpired(cmd="docker version", timeout=5)

    monkeypatch.setattr(docker_env.subprocess, "run", _timeout)
    with pytest.raises(EnvironmentConnectionError):
        docker_env._ensure_docker_available()

def test_missing_runtime_returns_degraded_result(isolated_env, monkeypatch):
    monkeypatch.setenv("TERMINAL_ENV", "docker")
    monkeypatch.delenv("TERMINAL_DEGRADED_MODE", raising=False)
    monkeypatch.setattr("tools.environments.docker._maybe_reap_docker_orphans", lambda _cc: None)
    monkeypatch.setattr("tools.environments.docker.find_docker", lambda: None)

    r = json.loads(isolated_env.terminal_tool("echo hi", task_id="t-degraded-docker"))
    assert r["status"] == "degraded"
    assert r.get("reason")
    assert r.get("retry_hint")
    assert "traceback" not in r
