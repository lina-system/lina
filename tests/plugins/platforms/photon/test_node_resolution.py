"""Photon sidecar consumers use the PM Node and npm closure."""
import json
import shutil
import subprocess
from pathlib import Path
import pytest
import pm
pytest_plugins = ["tests.pm.test_node_resolution"]

@pytest.fixture
def photon_npm_consumers(npm_probe, tmp_path, monkeypatch):
    from plugins.platforms.photon import adapter as photon, cli

    _home, _node, _binary, _publish = npm_probe
    bridge = tmp_path / "bridge"
    bridge.mkdir()
    (bridge / "package.json").write_text('{"name":"test-bridge"}', encoding="utf-8")
    monkeypatch.setattr(photon, "_sidecar_dir", lambda: bridge)
    monkeypatch.setattr(cli, "_sidecar_dir", lambda: bridge)
    return {
        "photon": (photon._reinstall_sidecar_deps, bridge),
        "photon-cli": (cli._install_sidecar, bridge),
    }


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("consumer", ["photon", "photon-cli"])
def test_photon_npm_consumers_execute_with_pm_node(npm_probe, photon_npm_consumers, consumer):
    _home, node, binary, publish = npm_probe
    publish()
    call, output_dir = photon_npm_consumers[consumer]
    call()
    result = json.loads((output_dir / "called.json").read_text())
    assert Path(result["argv"][1]) == binary
    assert shutil.which("node", path=result["env"]["PATH"]) == str(node)


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("surface", ["photon", "photon-cli"])
def test_missing_npm_acquires_npm_closure_at_install_boundary(npm_probe, photon_npm_consumers, monkeypatch, surface):
    _home, node, binary, publish = npm_probe
    calls = []

    def ensure(name, **kwargs):
        calls.append((name, kwargs.get("explicit", False)))
        assert name == "npm"
        return publish()

    monkeypatch.setattr(pm, "ensure", ensure)
    call, bridge = photon_npm_consumers[surface]
    call()
    assert calls == [("npm", surface in {"cli", "dashboard", "photon-cli"})]
    result = json.loads((bridge / "called.json").read_text())
    assert Path(result["argv"][1]) == binary
    assert shutil.which("node", path=result["env"]["PATH"]) == str(node)


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("surface", ["photon"])
def test_runtime_npm_refusal_never_falls_back_to_literal_npm(npm_probe, photon_npm_consumers, monkeypatch, surface):
    call, bridge = photon_npm_consumers[surface]
    spawns = []
    run = subprocess.run

    def forbidden_spawn(*args, **kwargs):
        if args[0][0] == "git":  # Fatal-status reporting may read the code revision.
            return run(*args, **kwargs)
        spawns.append(args)
        raise AssertionError("refused PM preparation must not spawn npm")

    monkeypatch.setattr(subprocess, "run", forbidden_spawn)
    call()
    assert spawns == []
    assert not (bridge / "called.json").exists()
    assert pm.installed_package("npm") is None


@pytest.mark.parametrize("allowed", [False, True])
def test_adapter_availability_never_provisions_missing_node(tmp_path, monkeypatch, allowed):
    from plugins.platforms.photon import adapter as photon

    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    monkeypatch.setenv("HERMES_RUNTIME_DIR", str(tmp_path / "missing-tools"))
    monkeypatch.setenv("PATH", "")
    monkeypatch.delenv("PHOTON_NODE_BIN", raising=False)
    monkeypatch.setattr(photon, "_sidecar_dir", lambda: tmp_path)
    monkeypatch.setattr(photon, "HTTPX_AVAILABLE", True)
    monkeypatch.setattr(pm, "lazy_installs_allowed", lambda: allowed)
    installs = []

    def forbidden_ensure(*args, **kwargs):
        installs.append(args)
        raise AssertionError("availability must not install")

    monkeypatch.setattr(pm, "ensure", forbidden_ensure)
    assert photon.check_requirements() is allowed
    assert installs == []
    assert not (tmp_path / "missing-tools").exists()
