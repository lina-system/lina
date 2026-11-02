"""The ``ocx`` service (lina#32): the generated launchd plist / systemd unit run the pinned bun and OCX with
``OPENCODEX_HOME`` set, ``lina services start ocx`` brings it up on ``ocx.port``, and a port held by anything else
is refused before a unit is written."""
import argparse
import json
import plistlib
import shlex
import socket
import subprocess

import pytest

from hermes_cli import lina_ocx, lina_ocx_service
from hermes_cli.subcommands.services import build_services_parser
from tests.hermes_cli._lina_ocx_fakes import install_fake_version, installed_root, other_ocx


@pytest.fixture
def root(tmp_path, monkeypatch):
    root = installed_root(tmp_path / "lina")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    return root


def test_ocx_port_is_a_registered_setting_that_config_get_and_the_service_read_alike(root, monkeypatch, capsys):
    from hermes_cli.config import get_config_value
    monkeypatch.setenv("HERMES_HOME", str(root))
    get_config_value("ocx.port")
    assert capsys.readouterr().out.strip() == str(lina_ocx.configured_port(root)) == "10100"
    (root / "config.yaml").write_text("ocx:\n  port: 18123\n")
    get_config_value("ocx.port")
    assert capsys.readouterr().out.strip() == str(lina_ocx.configured_port(root)) == "18123"


def test_systemd_unit_runs_the_pinned_bun_and_ocx_with_its_data_dir(root):
    unit = lina_ocx_service.definition("systemd", root, "2.95.0", 10100).splitlines()
    (exec_start,) = [line for line in unit if line.startswith("ExecStart=")]
    bun, entry = lina_ocx.entry_paths(root, "2.95.0")
    assert shlex.split(exec_start.removeprefix("ExecStart=")) == [str(bun), str(entry), "start", "--port", "10100"]
    assert str(bun).startswith(str(root / "vendor" / "ocx" / "2.95.0"))
    assert f'Environment="OPENCODEX_HOME={root / "ocx"}"' in unit
    assert f'Environment="HOME={root / "vendor" / "ocx" / "home"}"' in unit
    assert "Description=LINA lina-ocx" in unit


def test_launchd_plist_is_ai_lina_ocx_with_the_pinned_paths(root):
    plist = plistlib.loads(lina_ocx_service.definition("launchd", root, "2.95.0", 10123).encode())
    assert plist["Label"] == "ai.lina.ocx"
    assert plist["ProgramArguments"] == lina_ocx.ocx_argv(root, "2.95.0", "start", "--port", "10123")
    assert plist["EnvironmentVariables"]["OPENCODEX_HOME"] == str(root / "ocx")
    assert not plist["Label"].startswith("com.opencodex")


class _Systemd:
    """``systemctl --user`` for the test: ``restart lina-ocx`` runs the written unit's ExecStart with its
    Environment, ``stop`` ends it."""

    def __init__(self, unit_path):
        self.unit_path, self.calls, self.proc = unit_path, [], None

    def __call__(self, argv):
        self.calls.append(argv)
        if argv[-2:] == ["restart", "lina-ocx"]:
            lines = self.unit_path.read_text().splitlines()
            env = dict(line.removeprefix('Environment="').rstrip('"').split("=", 1)
                       for line in lines if line.startswith("Environment="))
            (cmd,) = [line.removeprefix("ExecStart=") for line in lines if line.startswith("ExecStart=")]
            self.proc = subprocess.Popen(shlex.split(cmd), env=env)
        if argv[-2:] == ["stop", "lina-ocx"] and self.proc:
            self.proc.terminate()
            self.proc.wait(timeout=10)
        return subprocess.CompletedProcess(argv, 0, "", "")


@pytest.fixture
def systemd(root, tmp_path, monkeypatch):
    fake = _Systemd(tmp_path / "units" / "lina-ocx.service")
    monkeypatch.setattr(lina_ocx_service, "definition_path", lambda kind: fake.unit_path)
    monkeypatch.setattr(lina_ocx_service, "_run", fake)
    yield fake
    if fake.proc and fake.proc.poll() is None:
        fake.proc.kill()
        fake.proc.wait()


def _services(*argv) -> int:
    parser = argparse.ArgumentParser()
    build_services_parser(parser.add_subparsers())
    args = parser.parse_args(["services", *argv])
    with pytest.raises(SystemExit) as exc:
        args.func(args)
    return exc.value.code


@pytest.mark.platforms("linux")
def test_lina_services_start_ocx_runs_it_on_the_configured_port(root, systemd, capsys):
    port = lina_ocx.free_port()
    (root / "config.yaml").write_text(f"ocx:\n  port: {port}\n")
    assert _services("start", "ocx") == 0
    assert lina_ocx.own_proxy_running(root, port)
    assert ["systemctl", "--user", "restart", "lina-ocx"] in systemd.calls
    assert f"--port {port}" in systemd.unit_path.read_text()
    assert _services("list") == 0
    rows = {line.split(None, 2)[0]: line.split(None, 2)[1:] for line in capsys.readouterr().out.splitlines()}
    assert rows["ocx"] == ["lina-ocx", f"running (2.95.0, port {port})"]
    assert _services("stop", "ocx") == 0
    assert not lina_ocx.own_proxy_running(root, port)
    joined = " ".join(" ".join(call) for call in systemd.calls)
    assert "com.opencodex" not in joined and "service install" not in joined


@pytest.mark.platforms("linux")
def test_start_on_an_empty_data_dir_succeeds_beside_another_ocx_on_the_default_port(root, systemd, monkeypatch):
    """OCX 2.95.0 probes its config's port (else the default) before it binds --port and refuses a start when
    another OCX answers there; LINA's start must point that probe at its own port."""
    monkeypatch.setattr(lina_ocx_service, "START_WAIT_S", 10.0)
    with other_ocx() as default_port:
        install_fake_version(root, "2.95.0", default_port=default_port)
        port = lina_ocx.free_port()
        (root / "config.yaml").write_text(f"ocx:\n  port: {port}\n")
        assert lina_ocx_service.start(root) == 0
        assert lina_ocx.own_proxy_running(root, port)
    assert json.loads((root / "ocx" / "config.json").read_text())["port"] == port


def test_the_start_port_is_merged_into_ocx_config_keeping_every_other_key(tmp_path):
    data = tmp_path / "ocx"
    data.mkdir()
    (data / "config.json").write_text('{"port": 10100, "defaultProvider": "openai", "providers": {"openai": {}}}')
    lina_ocx.write_start_port(data, 10150)
    assert json.loads((data / "config.json").read_text()) == {"port": 10150, "defaultProvider": "openai",
                                                               "providers": {"openai": {}}}
    lina_ocx.write_start_port(tmp_path / "fresh", 10151)
    assert json.loads((tmp_path / "fresh" / "config.json").read_text()) == {"port": 10151}
    (data / "config.json").write_text("{not json")
    with pytest.raises(lina_ocx.OcxError, match="not JSON"):
        lina_ocx.write_start_port(data, 10152)
    assert (data / "config.json").read_text() == "{not json"


@pytest.mark.platforms("linux")
def test_start_refuses_a_port_held_by_another_process_naming_it(root, systemd, capsys):
    with socket.socket() as other:
        other.bind(("127.0.0.1", 0))
        other.listen()
        port = other.getsockname()[1]
        (root / "config.yaml").write_text(f"ocx:\n  port: {port}\n")
        assert _services("start", "ocx") == 1
    out = capsys.readouterr().out.splitlines()
    assert len(out) == 1 and f"port {port} is already in use" in out[0]
    assert systemd.calls == [] and not systemd.unit_path.exists()


def test_state_reads_not_installed_then_stopped(tmp_path, monkeypatch):
    bare = tmp_path / "bare"
    assert lina_ocx_service.state(bare) == "not installed"
    installed_root(bare)
    (bare / "config.yaml").write_text(f"ocx:\n  port: {lina_ocx.free_port()}\n")
    assert lina_ocx_service.state(bare) == "stopped (2.95.0)"
