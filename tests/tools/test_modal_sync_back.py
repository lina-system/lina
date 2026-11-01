"""Cloud environment download and cleanup contracts."""
import asyncio
from unittest.mock import AsyncMock, MagicMock
from tools.environments import modal as modal_env
from tools.environments import daytona as daytona_env

def _make_mock_modal_env():
    """Create a minimal ModalEnvironment without calling __init__."""
    env = object.__new__(modal_env.ModalEnvironment)
    env._sandbox = MagicMock()
    env._worker = MagicMock()
    env._persistent = False
    env._task_id = "test"
    env._sync_manager = None
    return env

def _wire_modal_download(env, *, tar_bytes=b"fake-tar-data", exit_code=0):
    """Wire sandbox.exec.aio to return mock tar output for download tests.

    Returns the exec_calls list for assertion.
    """
    exec_calls = []

    async def mock_exec_fn(*args, **kwargs):
        exec_calls.append(args)
        proc = MagicMock()
        proc.stdout = MagicMock()
        proc.stdout.read = MagicMock()
        proc.stdout.read.aio = AsyncMock(return_value=tar_bytes)
        proc.wait = MagicMock()
        proc.wait.aio = AsyncMock(return_value=exit_code)
        return proc

    env._sandbox.exec = MagicMock()
    env._sandbox.exec.aio = mock_exec_fn

    def real_run_coroutine(coro, **kwargs):
        loop = asyncio.new_event_loop()
        try:
            return loop.run_until_complete(coro)
        finally:
            loop.close()

    env._worker.run_coroutine = real_run_coroutine
    return exec_calls

# ── Daytona helpers ──────────────────────────────────────────────────

def _make_mock_daytona_env():
    """Create a minimal DaytonaEnvironment without calling __init__."""
    env = object.__new__(daytona_env.DaytonaEnvironment)
    env._sandbox = MagicMock()
    env._remote_home = "/root"
    env._sync_manager = None
    env._lock = __import__("threading").Lock()
    env._persistent = True
    env._task_id = "test"
    env._daytona = MagicMock()
    return env

class TestModalBulkDownload:
    """Unit tests for _modal_bulk_download."""

    def test_modal_bulk_download_command(self, tmp_path):
        """exec should be called with tar cf - -C /root/.hermes ."""
        env = _make_mock_modal_env()
        exec_calls = _wire_modal_download(env, tar_bytes=b"tar-content")
        dest = tmp_path / "backup.tar"

        env._modal_bulk_download(dest)

        assert len(exec_calls) == 1
        args = exec_calls[0]
        assert args[0] == "bash"
        assert args[1] == "-c"
        assert "tar cf -" in args[2]
        assert "-C / root/.hermes" in args[2]
        # Live sockets cannot be archived; exclude them like the SSH backend.
        assert "--exclude='*.sock'" in args[2]

class TestModalCleanup:
    """Verify Modal cleanup() calls sync_back() before terminate."""

    def test_modal_cleanup_calls_sync_back(self):
        """cleanup() should call sync_back() before sandbox.terminate."""
        env = _make_mock_modal_env()

        call_order = []
        sync_mgr = MagicMock()
        sync_mgr.sync_back = lambda: call_order.append("sync_back")
        env._sync_manager = sync_mgr

        # Mock terminate to track call order
        async def mock_terminate():
            pass

        env._sandbox.terminate = MagicMock()
        env._sandbox.terminate.aio = mock_terminate
        env._worker.run_coroutine = lambda coro, **kw: (
            call_order.append("terminate"),
            asyncio.new_event_loop().run_until_complete(coro),
        )
        env._worker.stop = lambda: None

        env.cleanup()

        assert "sync_back" in call_order
        assert call_order.index("sync_back") < call_order.index("terminate")

# =====================================================================
# Daytona bulk download
# =====================================================================

class TestDaytonaBulkDownload:
    """Unit tests for _daytona_bulk_download."""

    def test_daytona_bulk_download_creates_tar_and_downloads(self, tmp_path):
        """exec and download_file should both be called."""
        env = _make_mock_daytona_env()
        dest = tmp_path / "backup.tar"

        env._daytona_bulk_download(dest)

        # exec called twice: tar creation + rm cleanup
        assert env._sandbox.process.exec.call_count == 2
        tar_cmd = env._sandbox.process.exec.call_args_list[0][0][0]
        assert "tar cf" in tar_cmd
        # Live sockets cannot be archived; exclude them like the SSH backend.
        assert "--exclude='*.sock'" in tar_cmd
        # PID-suffixed temp path avoids collisions on sync_back retry
        assert "/tmp/.hermes_sync." in tar_cmd
        assert ".tar" in tar_cmd
        assert ".hermes" in tar_cmd

        cleanup_cmd = env._sandbox.process.exec.call_args_list[1][0][0]
        assert "rm -f" in cleanup_cmd
        assert "/tmp/.hermes_sync." in cleanup_cmd

        # download_file called once with the same PID-suffixed path
        env._sandbox.fs.download_file.assert_called_once()
        download_args = env._sandbox.fs.download_file.call_args[0]
        assert download_args[0].startswith("/tmp/.hermes_sync.")
        assert download_args[0].endswith(".tar")
        assert download_args[1] == str(dest)

    def test_daytona_bulk_download_uses_remote_home(self, tmp_path):
        """The tar command should use the env's _remote_home."""
        env = _make_mock_daytona_env()
        env._remote_home = "/home/daytona"
        dest = tmp_path / "backup.tar"

        env._daytona_bulk_download(dest)

        tar_cmd = env._sandbox.process.exec.call_args_list[0][0][0]
        assert "home/daytona/.hermes" in tar_cmd

class TestDaytonaCleanup:
    """Verify Daytona cleanup() calls sync_back() before stop."""

    def test_daytona_cleanup_calls_sync_back(self):
        """cleanup() should call sync_back() before sandbox.stop()."""
        env = _make_mock_daytona_env()

        call_order = []
        sync_mgr = MagicMock()
        sync_mgr.sync_back = lambda: call_order.append("sync_back")
        env._sync_manager = sync_mgr
        env._sandbox.stop = lambda: call_order.append("stop")

        env.cleanup()

        assert "sync_back" in call_order
        assert "stop" in call_order
        assert call_order.index("sync_back") < call_order.index("stop")
