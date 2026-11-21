"""Preview the selected blob over Git's real remote archive protocol."""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
from typing import Final

import pytest

pytestmark = pytest.mark.platforms("posix")

FILE: Final = "docs/미디어.bin"
SIZE: Final = 16 * 1024 * 1024

# Sit between Git's archive client and the real upload-archive server and count
# bytes forwarded to the client, including protocol framing and read-ahead.
ARCHIVE_PROXY: Final = """
import os
import subprocess
import sys

git, repo = sys.argv[1:]
with subprocess.Popen([git, "upload-archive", repo], stdout=subprocess.PIPE) as server:
    with open(os.environ["PREVIEW_TRANSFER_TRACE"], "ab", buffering=0) as trace:
        while packet := os.read(server.stdout.fileno(), 4096):
            count = os.write(1, packet)
            trace.write(f"{count}\\n".encode("ascii"))
    sys.exit(server.wait())
"""


def _git(cwd: Path, *args: str) -> bytes:
    return subprocess.run(
        ["git", "-C", str(cwd), *args], check=True, capture_output=True, timeout=20,
    ).stdout


@dataclass(frozen=True, slots=True)
class Repository:
    url: str
    commit: str
    contents: bytes


@pytest.fixture(scope="module")
def repository(tmp_path_factory: pytest.TempPathFactory) -> Repository:
    repo = tmp_path_factory.mktemp("preview-remote")
    _git(repo, "init", "--initial-branch=main")
    _git(repo, "config", "uploadpack.allowFilter", "false")
    contents = hashlib.shake_256(b"selected-media-fixture").digest(SIZE)
    (repo / "docs").mkdir()
    (repo / FILE).write_bytes(contents)
    (repo / "docs/link").symlink_to("미디어.bin")
    (repo / "docs/hidden").write_text("excluded content", encoding="utf-8")
    (repo / ".gitattributes").write_text("docs/hidden export-ignore\n", encoding="utf-8")
    _git(repo, "add", ".")
    _git(repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Fixture")
    return Repository(repo.as_uri(), _git(repo, "rev-parse", "HEAD").decode().strip(), contents)


@pytest.fixture
def archive_helper(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    monkeypatch.setenv("GIT_EXEC_PATH", str(tmp_path))
    return tmp_path / "git-upload-archive"


def _executable(path: Path, command: str) -> None:
    path.write_text(f"#!/bin/sh\nexec {command}\n", encoding="utf-8")
    path.chmod(0o755)


def test_selected_large_blob_preview_stops_remote_transfer_early(
    repository: Repository, archive_helper: Path, monkeypatch: pytest.MonkeyPatch, tmp_path: Path,
) -> None:
    from hermes_cli.work.forge_preview import preview

    git = shutil.which("git")
    assert git is not None
    cmd = shlex.join([sys.executable, "-c", ARCHIVE_PROXY, git])
    _executable(archive_helper, cmd + ' "$@"')
    full_trace = tmp_path / "full"
    monkeypatch.setenv("PREVIEW_TRANSFER_TRACE", str(full_trace))
    _git(tmp_path, "archive", "--format=tar", f"--remote={repository.url}", "HEAD", "--", FILE)
    full_bytes = sum(map(int, full_trace.read_text(encoding="ascii").splitlines()))
    assert full_bytes >= SIZE

    trace = tmp_path / "preview"
    monkeypatch.setenv("PREVIEW_TRANSFER_TRACE", str(trace))
    limit = 4099
    assert preview(repository.url, FILE, limit) == (repository.commit, repository.contents[:limit], SIZE)
    preview_bytes = sum(map(int, trace.read_text(encoding="ascii").splitlines()))
    assert limit <= preview_bytes < full_bytes // 8, (preview_bytes, full_bytes)


@pytest.mark.parametrize("path", ["docs", "docs/link", "docs/missing", "docs/hidden"])
def test_preview_rejects_paths_missing_from_the_regular_archive(repository: Repository, path: str) -> None:
    from hermes_cli.work.forge_preview import ForgeError, preview

    with pytest.raises(ForgeError) as error:
        preview(repository.url, path, 12)
    assert error.value.status == 404


@pytest.mark.parametrize("path", ["../secret", "/absolute", "-o", ":(glob)docs/*", "docs/[ab]", "docs/\ud800"])
def test_preview_rejects_unsafe_or_non_utf8_paths(path: str) -> None:
    from hermes_cli.work.forge_preview import ForgeError, preview

    with pytest.raises(ForgeError) as error:
        preview("file:///nonexistent", path, 12)
    assert error.value.status == 400


def test_preview_reports_unsupported_transport() -> None:
    from hermes_cli.work.forge_preview import ForgeError, preview

    with pytest.raises(ForgeError) as error:
        preview("https://example.invalid/repo.git", FILE, 12)
    assert error.value.status in {501, 502}


def test_stop_archive_tolerates_eperm_from_an_exited_group_leader(monkeypatch: pytest.MonkeyPatch) -> None:
    from hermes_cli.work import forge_preview

    process = subprocess.Popen([sys.executable, "-c", "pass"], start_new_session=True)
    process.wait()

    def eperm(*_args: object) -> None:
        raise PermissionError(1, "Operation not permitted")

    monkeypatch.setattr(forge_preview.os, "killpg", eperm)
    forge_preview._stop_archive(process)
    assert process.returncode == 0


def test_preview_times_out_when_archive_server_stalls(
    repository: Repository, archive_helper: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    from hermes_cli.work import forge_preview

    _executable(archive_helper, "sleep 30")
    monkeypatch.setattr(forge_preview, "_TIMEOUT_SECONDS", 0.5)
    with pytest.raises(forge_preview.ForgeError) as error:
        forge_preview.preview(repository.url, FILE, 12)
    assert error.value.status == 504
