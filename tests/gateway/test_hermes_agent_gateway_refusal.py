"""A LINA profile refuses to start its gateway while Hermes Agent's gateway for that profile name is loaded (lina#31).

Both gateways would poll the same bot tokens. The probe is read-only: ``launchctl print`` / ``systemctl --user
is-active``; the refusal is exit 78 (launchd and systemd do not respawn it) and ``--force`` does not skip it.
"""
import os
import subprocess

import pytest

from gateway.restart import GATEWAY_FATAL_CONFIG_EXIT_CODE

MUTATING = ("stop", "unload", "bootout", "disable", "kill", "kickstart", "remove")


@pytest.fixture
def lina(tmp_path, monkeypatch):
    root = tmp_path / ".lina"
    (root / "profiles" / "coder").mkdir(parents=True)
    (root / "config.yaml").write_text("model: {default: m}\n")
    (root / "profiles" / "coder" / "config.yaml").write_text("model: {default: m}\n")
    monkeypatch.setenv("HERMES_HOME", str(root / "profiles" / "coder"))
    monkeypatch.delenv("LINA_TEST_NO_HERMES_AGENT_GATEWAY", raising=False)
    import hermes_constants
    hermes_constants._default_hermes_root_memo = None
    monkeypatch.setattr("hermes_cli.resource_limits.apply_nofile_soft_limit", lambda: None)
    monkeypatch.setattr("gateway.code_skew.record_boot_fingerprint", lambda: None)
    monkeypatch.setattr("gateway.run_bootstrap._start_gateway_early_environment_probe", lambda: None)
    status = []
    monkeypatch.setattr("gateway.run._write_runtime_status_quiet", lambda **kw: status.append(kw))
    reached = []

    async def attach(replace, force=False):
        reached.append(force)
        return True  # the first step after the refusal: answering here means the launch went on

    monkeypatch.setattr("gateway.run._host_attach_or_none", attach)
    return {"root": root, "status": status, "reached": reached}


def _account(monkeypatch, loaded: set[str]) -> list[list[str]]:
    """Fake host service managers: ``loaded`` names answer as loaded; every probe argv is recorded."""
    calls = []

    def probe(argv):
        calls.append(list(argv))
        target = argv[-1]
        if argv[0] == "launchctl":
            return subprocess.CompletedProcess(argv, 0 if target.split("/")[-1] in loaded else 113, "", "")
        return subprocess.CompletedProcess(argv, 0 if target in loaded else 3,
                                           "active\n" if target in loaded else "inactive\n", "")

    monkeypatch.setattr("hermes_cli.lina_services._probe", probe)
    return calls


def _read_only(calls) -> bool:
    return bool(calls) and not any(word in arg for call in calls for arg in call for word in MUTATING)


async def _start(force=False):
    from gateway import run_bootstrap
    from gateway.config import GatewayConfig
    return await run_bootstrap.start_gateway(config=GatewayConfig(), force=force)


@pytest.mark.platforms("linux")
@pytest.mark.asyncio
@pytest.mark.parametrize("force", [False, True])
async def test_linux_refuses_beside_the_hermes_agent_unit(lina, monkeypatch, capsys, force):
    calls = _account(monkeypatch, {"hermes-gateway-coder"})
    with pytest.raises(SystemExit) as exc:
        await _start(force=force)
    assert exc.value.code == GATEWAY_FATAL_CONFIG_EXIT_CODE
    out = capsys.readouterr().out.strip().splitlines()
    assert len(out) == 1 and "hermes-gateway-coder.service" in out[0] and "'coder'" in out[0]
    assert calls == [["systemctl", "--user", "is-active", "hermes-gateway-coder"]] and _read_only(calls)
    assert lina["reached"] == [] and lina["status"][-1]["exit_reason"] == "hermes_agent_gateway_loaded"


@pytest.mark.platforms("posix")
@pytest.mark.asyncio
async def test_only_the_probe_seam_gets_past_a_loaded_hermes_agent_gateway(lina, monkeypatch):
    """``LINA_TEST_NO_HERMES_AGENT_GATEWAY=1`` (set by the gateway test probes for their child, lina#259) is the one
    way around the guard: without it a loaded answer still refuses, even with ``--force``."""
    calls = _account(monkeypatch, {"hermes-gateway-coder", "ai.hermes.gateway-coder"})
    with pytest.raises(SystemExit) as exc:
        await _start(force=True)
    assert exc.value.code == GATEWAY_FATAL_CONFIG_EXIT_CODE and lina["reached"] == []

    probed = len(calls)
    monkeypatch.setenv("LINA_TEST_NO_HERMES_AGENT_GATEWAY", "1")
    assert await _start() is True
    assert lina["reached"] == [False] and len(calls) == probed


@pytest.mark.platforms("linux")
@pytest.mark.asyncio
async def test_linux_starts_when_no_hermes_agent_gateway_is_loaded(lina, monkeypatch):
    calls = _account(monkeypatch, {"hermes-gateway", "hermes-gateway-other"})
    assert await _start() is True
    assert lina["reached"] == [False] and _read_only(calls)


@pytest.mark.platforms("linux")
@pytest.mark.asyncio
async def test_linux_multiplexer_refuses_for_a_served_profile(lina, monkeypatch):
    """The default profile's multiplexer serves ``coder`` too, so Hermes Agent's ``coder`` unit blocks it."""
    monkeypatch.setenv("HERMES_HOME", str(lina["root"]))
    import hermes_constants
    hermes_constants._default_hermes_root_memo = None
    monkeypatch.setattr("gateway.run._multiplex_profile_homes",
                        lambda config: [("coder", lina["root"] / "profiles" / "coder")])
    monkeypatch.setattr("gateway.run_bootstrap._launch_home_may_multiplex", lambda config=None: True)
    calls = _account(monkeypatch, {"hermes-gateway-coder"})
    from gateway import run_bootstrap
    from gateway.config import GatewayConfig
    config = GatewayConfig()
    config.multiplex_profiles = True
    with pytest.raises(SystemExit) as exc:
        await run_bootstrap.start_gateway(config=config)
    assert exc.value.code == GATEWAY_FATAL_CONFIG_EXIT_CODE
    assert [call[-1] for call in calls] == ["hermes-gateway", "hermes-gateway-coder"] and _read_only(calls)


@pytest.mark.platforms("macos")
@pytest.mark.asyncio
@pytest.mark.parametrize("force", [False, True])
async def test_macos_refuses_beside_the_hermes_agent_launch_agent(lina, monkeypatch, capsys, force):
    calls = _account(monkeypatch, {"ai.hermes.gateway-coder"})
    with pytest.raises(SystemExit) as exc:
        await _start(force=force)
    assert exc.value.code == GATEWAY_FATAL_CONFIG_EXIT_CODE
    out = capsys.readouterr().out.strip().splitlines()
    assert len(out) == 1 and "ai.hermes.gateway-coder" in out[0]
    assert calls == [["launchctl", "print", f"gui/{os.getuid()}/ai.hermes.gateway-coder"]] and _read_only(calls)
    assert lina["reached"] == []


@pytest.mark.platforms("macos")
@pytest.mark.asyncio
async def test_macos_starts_when_no_hermes_agent_gateway_is_loaded(lina, monkeypatch):
    calls = _account(monkeypatch, {"ai.hermes.gateway"})
    assert await _start() is True
    uid = os.getuid()
    assert calls == [["launchctl", "print", f"gui/{uid}/ai.hermes.gateway-coder"],
                     ["launchctl", "print", f"user/{uid}/ai.hermes.gateway-coder"]]
    assert lina["reached"] == [False] and _read_only(calls)
