"""Every launchd job LINA writes sets ``MaterializeDatalessFiles`` (lina-system/lina#37).

launchd jobs do not download cloud placeholders by default, so a job opening a not yet downloaded file of a synced
Desk (and every child process it starts, which inherits the setting) would fail. The gateway plist already set it;
the services written through ``service_definition`` (``ai.lina.worker``) and the one-off gateway job
must too. The plists are generated and read back; nothing is loaded.
"""

from __future__ import annotations

import plistlib
import sys

import pytest

from hermes_cli import gateway_launchd_runtime, lina_services
from hermes_cli.worker import service as worker_service


def _worker(tmp_path) -> dict:
    return plistlib.loads(worker_service.definition("launchd", tmp_path).encode())


def _seam(tmp_path) -> dict:
    return plistlib.loads(lina_services.service_definition(
        "launchd", "ai.lina.any", ["/bin/true"], {}, logs=lina_services.service_logs(tmp_path, "any")).encode())


@pytest.mark.parametrize("plist", [_worker, _seam], ids=["worker", "service_definition"])
def test_service_plists_materialize_dataless_files(tmp_path, plist):
    data = plist(tmp_path)
    assert data["MaterializeDatalessFiles"] is True
    assert data["Label"].startswith("ai.lina.")


def test_the_systemd_unit_is_unchanged_by_the_launchd_key(tmp_path):
    unit = lina_services.service_definition("systemd", "lina-worker", ["/bin/true"], {},
                                            logs=lina_services.service_logs(tmp_path, "worker"))
    assert "Materialize" not in unit


def test_the_one_off_gateway_job_materializes_dataless_files(tmp_path, monkeypatch):
    seen: list[dict] = []

    def launchctl(*args, timeout):
        if args[0] == "bootstrap":
            with open(args[2], "rb") as fh:
                seen.append(plistlib.load(fh))

    monkeypatch.setattr(gateway_launchd_runtime, "_launchctl", launchctl)
    monkeypatch.setattr(gateway_launchd_runtime.os, "getuid", lambda: 501, raising=False)
    # The real launch path: the conftest's test-isolation marker keeps a test run out of launchd (#372).
    monkeypatch.delenv("HERMES_TEST_ISOLATION", raising=False)
    gateway_launchd_runtime.start_gateway_job([sys.executable, "-c", "pass"], env={}, cwd=tmp_path,
                                              log_path=tmp_path / "gateway.log")
    assert len(seen) == 1 and seen[0]["MaterializeDatalessFiles"] is True
