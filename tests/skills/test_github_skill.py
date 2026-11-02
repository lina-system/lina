"""github's gh-env.sh picks the auth method and the repo the way the skill tells the agent.

The script is sourced by the agent before API calls; ``GH_AUTH_METHOD`` decides between ``gh`` and
raw ``curl``. Runs with a PATH holding only stub ``gh``/``curl`` binaries, so no network is touched.
"""

from __future__ import annotations

import os
import shutil
import subprocess
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / "skills" / "software-development" / "github" / "scripts" / "gh-env.sh"

pytestmark = [
    pytest.mark.platforms("posix"),
    pytest.mark.skipif(shutil.which("bash") is None or shutil.which("git") is None,
                       reason="the helper is a bash script that reads the git remote"),
]


def _stub(bin_dir: Path, name: str, body: str) -> None:
    path = bin_dir / name
    path.write_text("#!/bin/sh\n" + body, encoding="utf-8")
    path.chmod(0o755)


def _source(tmp_path: Path, *, cwd: Path, env: dict[str, str] | None = None) -> dict[str, str]:
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir(exist_ok=True)
    for tool in ("git", "grep", "sed", "cut", "head", "tr", "dirname", "python3"):
        found = shutil.which(tool)
        if found and not (bin_dir / tool).exists():
            (bin_dir / tool).symlink_to(found)
    home = tmp_path / "home"
    home.mkdir(exist_ok=True)
    base = {"PATH": str(bin_dir), "HOME": str(home), "HERMES_HOME": str(home / ".hermes")}
    script = (f'source "{SCRIPT}" >/dev/null; for v in GH_AUTH_METHOD GH_USER GH_OWNER GH_REPO GH_OWNER_REPO; '
              'do printf "%s=%s\\n" "$v" "${!v}"; done; printf "TOKEN_SET=%s\\n" "${GITHUB_TOKEN:+yes}"')
    result = subprocess.run([shutil.which("bash"), "-c", script], cwd=cwd, env={**base, **(env or {})},
                            capture_output=True, text=True, encoding="utf-8", check=False, timeout=30)
    assert result.returncode == 0, result.stderr
    return dict(line.split("=", 1) for line in result.stdout.splitlines())


def _repo(tmp_path: Path, remote: str) -> Path:
    repo = tmp_path / "repo"
    repo.mkdir()
    subprocess.run(["git", "init", "-q"], cwd=repo, check=True, timeout=30)
    subprocess.run(["git", "remote", "add", "origin", remote], cwd=repo, check=True, timeout=30)
    return repo


@pytest.mark.parametrize("remote", ["git@github.com:lina-system/lina.git", "https://github.com/lina-system/lina"])
def test_reads_owner_and_repo_from_a_github_remote(tmp_path, remote):
    found = _source(tmp_path, cwd=_repo(tmp_path, remote))
    assert (found["GH_OWNER"], found["GH_REPO"], found["GH_OWNER_REPO"]) == ("lina-system", "lina", "lina-system/lina")


def test_no_credentials_means_none_and_no_token(tmp_path):
    found = _source(tmp_path, cwd=tmp_path)
    assert (found["GH_AUTH_METHOD"], found["TOKEN_SET"], found["GH_OWNER_REPO"]) == ("none", "", "")


def test_authenticated_gh_cli_wins(tmp_path):
    (tmp_path / "bin").mkdir()
    _stub(tmp_path / "bin", "gh", 'case "$1" in auth) exit 0;; api) echo octocat;; esac\n')
    found = _source(tmp_path, cwd=tmp_path, env={"GITHUB_TOKEN": "from-env"})
    assert (found["GH_AUTH_METHOD"], found["GH_USER"]) == ("gh", "octocat")


def test_token_in_hermes_env_selects_curl(tmp_path):
    (tmp_path / "bin").mkdir()
    _stub(tmp_path / "bin", "curl", 'echo \'{"login": "octocat"}\'\n')
    hermes = tmp_path / "home" / ".hermes"
    hermes.mkdir(parents=True)
    (hermes / ".env").write_text("OTHER=1\nGITHUB_TOKEN=ghp_from_dotenv\n", encoding="utf-8")
    found = _source(tmp_path, cwd=tmp_path)
    assert (found["GH_AUTH_METHOD"], found["GH_USER"], found["TOKEN_SET"]) == ("curl", "octocat", "yes")


def test_git_credentials_use_the_helper_next_to_the_script(tmp_path):
    (tmp_path / "bin").mkdir()
    _stub(tmp_path / "bin", "curl", 'echo \'{"login": "octocat"}\'\n')
    home = tmp_path / "home"
    home.mkdir()
    (home / ".git-credentials").write_text("https://octocat:ghp_from_store@github.com\n", encoding="utf-8")
    os.chmod(home / ".git-credentials", 0o600)
    found = _source(tmp_path, cwd=tmp_path)
    assert (found["GH_AUTH_METHOD"], found["TOKEN_SET"]) == ("curl", "yes")
