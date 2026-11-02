"""Photon runtime credentials are private even under a permissive umask."""
import os
import stat
from pathlib import Path
import pytest
pytest_plugins = ["tests.agent.test_private_credential_writers"]

@pytest.mark.platforms("posix")
def test_runtime_record_creates_private_temporary_file(tmp_path, monkeypatch, opens_spy):
    from plugins.platforms.photon import adapter
    target = tmp_path / "photon-sidecar.json"
    monkeypatch.setattr(adapter, "_runtime_record_path", lambda: target)
    previous = os.umask(0o022)
    try:
        adapter._write_runtime_record(1, "tok", 2)
        assert target.exists()
        assert stat.S_IMODE(target.stat().st_mode) == 0o600
        creates = [(p, m) for p, flags, m in opens_spy if Path(p).parent == target.parent]
        assert creates
        assert all(mode == 0o600 for path, mode in creates)
    finally:
        os.umask(previous)
