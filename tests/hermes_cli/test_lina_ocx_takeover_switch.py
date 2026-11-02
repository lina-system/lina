"""``lina models takeover switch`` and ``rehearse`` (lina#33) against fakes: a fake old OCX job driven by a fake
``systemctl --user``, fake listeners standing in for both OCX processes, and the fake pinned ``bun`` of lina#32 for
the rehearsal. A good switch keeps the listeners and the API token; a failed health check stops LINA's OCX, reloads
the old job and exits non-zero; the rehearsal never binds an old port and leaves the copy's config as it was."""
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from itertools import count
from pathlib import Path

import pytest

from hermes_cli import lina_ocx, lina_ocx_service, lina_ocx_takeover as takeover, lina_ocx_takeover_rehearse
from hermes_cli import lina_ocx_takeover_switch as switch
from hermes_cli.lina_ocx_takeover_refresh import JOB_NAME
from tests.hermes_cli._lina_ocx_fakes import FIXTURES, installed_root
from tests.hermes_cli.test_lina_ocx_takeover import TOKEN, _state, old_data_dir

_PIDS = count(700_001)


class FakeOcx:
    """Listeners of one OCX process: ``/healthz`` with its pid (recorded in *data*/ocx.pid when given) and
    ``/v1/models`` that needs the API token on a non-loopback listener."""

    def __init__(self, config: dict, token: str, data: Path | None = None, models_status: int = 200):
        self.pid, self.token, self.models_status, self.servers = next(_PIDS), token, models_status, []
        self.listeners = takeover.listeners(config)
        if data is not None:
            (data / "ocx.pid").write_text(str(self.pid))

    def _handler(self, listener):
        fake = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path == "/healthz":
                    code, body = 200, {"service": "opencodex", "pid": fake.pid}
                elif listener.token_required and self.headers.get("x-opencodex-api-key") != fake.token:
                    code, body = 401, {"error": "unauthorized"}
                else:
                    code, body = fake.models_status, {"object": "list", "data": []}
                payload = json.dumps(body).encode()
                self.send_response(code)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *args):
                pass

        return Handler

    def start(self):
        for item in self.listeners:
            server = ThreadingHTTPServer((item.host, item.port), self._handler(item))
            threading.Thread(target=server.serve_forever, daemon=True).start()
            self.servers.append(server)
        return self

    def stop(self):
        for server in self.servers:
            server.shutdown()
            server.server_close()
        self.servers = []


class FakeSystemctl:
    """``systemctl --user`` for the old job ``opencodex-proxy`` and LINA's ``lina-ocx``."""

    def __init__(self, source: Path, root: Path, lina_models_status: int):
        self.source, self.root, self.lina_models_status, self.calls, self.lina = source, root, lina_models_status, [], None
        self.old = self._old().start()

    def _old(self):
        return FakeOcx(json.loads((self.source / "config.json").read_text()), TOKEN)

    def __call__(self, argv):
        import subprocess
        self.calls.append(argv[2:])
        verb, unit = argv[-2:]
        if unit == "opencodex-proxy" and verb == "stop":
            self.old.stop()
        elif unit == "opencodex-proxy" and verb == "start":
            self.old = self._old().start()
        elif unit == "lina-ocx" and verb == "restart":
            data = lina_ocx.data_dir(self.root)
            config = json.loads((data / "config.json").read_text())
            token = (data / "service-api-token").read_text().strip()
            self.lina = FakeOcx(config, token, data, self.lina_models_status).start()
        elif unit == "lina-ocx" and verb == "stop" and self.lina:
            self.lina.stop()
        return subprocess.CompletedProcess(argv, 0, "", "")

    def close(self):
        self.old.stop()
        if self.lina:
            self.lina.stop()


def _ports() -> tuple[int, int]:
    return lina_ocx.free_port(), lina_ocx.free_port()


@pytest.fixture
def world(tmp_path, monkeypatch, request):
    root = installed_root(tmp_path / "lina")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    units = tmp_path / "units"
    units.mkdir()
    (units / "opencodex-proxy.service").write_text("[Service]\nExecStart=/bin/false\n")
    from hermes_cli import gateway
    monkeypatch.setattr(gateway, "user_systemd_unit_dir", lambda: units)
    monkeypatch.setattr(lina_ocx_service, "definition_path", lambda kind: units / "lina-ocx.service")
    monkeypatch.setattr(lina_ocx_service, "backend", lambda: "systemd")
    main_port, loopback_port = _ports()
    source = old_data_dir(tmp_path / "old", port=main_port, loopback_port=loopback_port)
    systemctl = FakeSystemctl(source, root, getattr(request, "param", 200))
    monkeypatch.setattr(switch, "_run", systemctl)
    monkeypatch.setattr(lina_ocx_service, "_run", systemctl)
    monkeypatch.setattr(switch, "HEALTH_WAIT_S", 3.0)
    yield {"root": root, "source": source, "systemctl": systemctl, "ports": (main_port, loopback_port)}
    systemctl.close()


def _models_ok(host, port, token=None) -> int:
    import urllib.error
    import urllib.request
    request = urllib.request.Request(f"http://{host}:{port}/v1/models")
    if token:
        request.add_header("x-opencodex-api-key", token)
    try:
        with urllib.request.urlopen(request, timeout=3) as response:
            return response.status
    except urllib.error.HTTPError as exc:
        return exc.code


def _healthz_pid(host, port) -> int:
    import urllib.request
    with urllib.request.urlopen(f"http://{host}:{port}/healthz", timeout=3) as response:
        return json.load(response)["pid"]


def _refresh_enabled(root) -> bool | None:
    from cron import jobs
    with jobs.use_cron_store(root):
        found = [j for j in jobs.list_jobs(include_disabled=True) if j["name"] == JOB_NAME]
    return found[0]["enabled"] if found else None


@pytest.mark.platforms("linux")
def test_a_good_switch_serves_the_old_listeners_from_lina_with_the_same_token(world):
    root, source, systemctl = world["root"], world["source"], world["systemctl"]
    main_port, loopback_port = world["ports"]
    before = _state(source)
    assert switch.switch(source, root=root, out=lambda line: None) == 0

    data = lina_ocx.data_dir(root)
    lina_pid = int((data / "ocx.pid").read_text())
    assert _healthz_pid("127.0.0.2", main_port) == lina_pid == _healthz_pid("127.0.0.1", loopback_port)
    assert _models_ok("127.0.0.2", main_port, TOKEN) == 200  # the agents' token, unchanged
    assert _models_ok("127.0.0.2", main_port) == 401
    assert _models_ok("127.0.0.1", loopback_port) == 200
    assert (data / "service-api-token").read_text().strip() == TOKEN
    assert lina_ocx.configured_port(root) == main_port
    assert f"--port {main_port}" in (lina_ocx_service.definition_path("systemd")).read_text()
    assert ["stop", "opencodex-proxy"] in systemctl.calls and ["disable", "opencodex-proxy"] in systemctl.calls
    assert ["start", "opencodex-proxy"] not in systemctl.calls
    assert _refresh_enabled(root) is True
    assert _state(source) == before


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("world", [500], indirect=True)
def test_a_failed_health_check_stops_lina_reloads_the_old_job_and_exits_non_zero(world):
    root, source, systemctl = world["root"], world["source"], world["systemctl"]
    main_port, loopback_port = world["ports"]
    before = _state(source)
    lines = []
    assert switch.switch(source, root=root, out=lines.append) == 1

    assert systemctl.calls.index(["stop", "lina-ocx"]) < systemctl.calls.index(["start", "opencodex-proxy"])
    old_pid = systemctl.old.pid
    assert _healthz_pid("127.0.0.2", main_port) == old_pid == _healthz_pid("127.0.0.1", loopback_port)
    assert _models_ok("127.0.0.2", main_port, TOKEN) == 200
    assert ["disable", "opencodex-proxy"] not in systemctl.calls
    assert lina_ocx.configured_port(root) == lina_ocx.DEFAULT_PORT
    assert _refresh_enabled(root) is not True
    assert _state(source) == before
    assert any("switch failed" in line for line in lines)


@pytest.mark.platforms("linux")
def test_switch_refuses_before_stopping_anything_when_the_old_job_cannot_be_reloaded(world, tmp_path):
    (tmp_path / "units" / "opencodex-proxy.service").unlink()
    assert switch.switch(world["source"], root=world["root"], out=lambda line: None) == 1
    assert world["systemctl"].calls == []
    assert _models_ok("127.0.0.1", world["ports"][1]) == 200


@pytest.fixture
def copied(tmp_path, monkeypatch):
    root = installed_root(tmp_path / "lina")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    monkeypatch.setattr(lina_ocx_takeover_rehearse, "REHEARSE_WAIT_S", 10.0)
    source = old_data_dir(tmp_path / "old", port=lina_ocx.free_port(), loopback_port=lina_ocx.free_port())
    assert takeover.copy(source, root, out=lambda line: None) == 0
    return root, source


@pytest.mark.platforms("posix")
def test_rehearse_runs_the_copy_on_a_spare_loopback_port_and_restores_its_config(copied):
    root, source = copied
    config_file = lina_ocx.data_dir(root) / "config.json"
    original = config_file.read_bytes()
    old_ports = {item.port for item in takeover.listeners(json.loads(original))}
    lines = []
    assert lina_ocx_takeover_rehearse.rehearse(source, FIXTURES / "account_list.json", root=root,
                                               out=lines.append) == 0
    argv = json.loads((lina_ocx.data_dir(root) / "argv").read_text())
    port = int(argv[argv.index("--port") + 1])
    assert port not in old_ports
    assert config_file.read_bytes() == original
    assert not lina_ocx.own_proxy_running(root, port)  # stopped at the end
    assert any("GET /v1/models 200" in line for line in lines)


def test_the_rehearsal_config_binds_loopback_only_and_turns_ocx_s_own_refreshers_off():
    config = {"port": 1, "hostname": "100.64.0.9", "unauthenticatedLoopbackListener": {"enabled": True, "port": 2},
              "runtimeRole": "hub", "hub": {"managementIngress": {"enabled": True, "port": 3}, "keep": 1},
              "tokenGuardian": {"enabled": True, "tickSeconds": 60}, "codexQuotaAutoRefresh": {"a": {"weekly": True}},
              "providers": {"x": {}}}
    edited = lina_ocx_takeover_rehearse.rehearsal_config(config, 4)
    assert [(item.host, item.port) for item in takeover.listeners(edited)] == [("127.0.0.1", 4)]
    assert edited["tokenGuardian"] == {"enabled": False, "tickSeconds": 60}
    assert edited["codexQuotaAutoRefresh"] == {} and edited["catalogAutoRefresh"]["enabled"] is False
    assert edited["quotaResetNotify"]["enabled"] is False and edited["hub"]["keep"] == 1
    assert edited["providers"] == config["providers"]


@pytest.mark.platforms("posix")
def test_rehearse_fails_when_the_copy_lists_another_account_count(copied):
    root, source = copied
    lines = []
    assert lina_ocx_takeover_rehearse.rehearse(source, expect=3, root=root, out=lines.append) == 1
    assert any("the source 3" in line for line in lines)
    assert os.path.exists(lina_ocx.data_dir(root) / "config.json")
