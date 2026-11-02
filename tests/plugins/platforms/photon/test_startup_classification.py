"""Photon sidecar startup failure classification."""
import asyncio
import pytest
from gateway.config import PlatformConfig

# ── Photon: typed sidecar startup errors ───────────────────────────────


class TestPhotonSidecarStartupClassification:
    def _make_adapter(self, monkeypatch):
        monkeypatch.setenv("PHOTON_PROJECT_ID", "pid")
        monkeypatch.setenv("PHOTON_PROJECT_SECRET", "psecret")
        from plugins.platforms.photon import adapter as photon_adapter
        from plugins.platforms.photon.adapter import PhotonAdapter

        # Stand-in for PM's node; the user's PATH node is never picked up.
        monkeypatch.setattr(photon_adapter, "find_node_executable", lambda _name: "/pm/node")

        return PhotonAdapter(PlatformConfig(enabled=True, token="", extra={}))

    @pytest.mark.asyncio
    async def test_typed_startup_error_sets_nonretryable_fatal(self, monkeypatch):
        from plugins.platforms.photon import adapter as photon_adapter

        adapter = self._make_adapter(monkeypatch)

        async def _boom():
            raise photon_adapter.PhotonSidecarStartupError(
                "deps could not be installed",
                code="SIDECAR_DEPS_MISSING",
                retryable=False,
            )

        monkeypatch.setattr(adapter, "_start_sidecar", _boom)
        ok = await adapter.connect()

        assert ok is False
        assert adapter.fatal_error_code == "SIDECAR_DEPS_MISSING"
        assert adapter.fatal_error_retryable is False

    @pytest.mark.asyncio
    async def test_untyped_startup_error_stays_retryable(self, monkeypatch):
        # Ambiguous failures (crash before ready, health timeout) must keep
        # retrying; the gateway's needs_attention escalation is the backstop.
        adapter = self._make_adapter(monkeypatch)

        async def _boom():
            raise RuntimeError("sidecar exited with code 1 before becoming ready")

        monkeypatch.setattr(adapter, "_start_sidecar", _boom)
        ok = await adapter.connect()

        assert ok is False
        assert adapter.fatal_error_code == "SIDECAR_FAILED"
        assert adapter.fatal_error_retryable is True

    @pytest.mark.asyncio
    async def test_deps_install_failure_raises_typed_nonretryable(self, monkeypatch):
        from plugins.platforms.photon import adapter as photon_adapter

        adapter = self._make_adapter(monkeypatch)
        monkeypatch.setattr(photon_adapter, "sidecar_deps_installed", lambda: False)
        monkeypatch.setattr(photon_adapter, "_reinstall_sidecar_deps", lambda: None)

        with pytest.raises(photon_adapter.PhotonSidecarStartupError) as exc_info:
            await adapter._start_sidecar()

        assert exc_info.value.code == "SIDECAR_DEPS_MISSING"
        assert exc_info.value.retryable is False
