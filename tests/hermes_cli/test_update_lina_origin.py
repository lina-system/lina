"""A lina-system/lina checkout updates from that origin only (lina-system/lina#79).

The pre-pull checkout phase must not prompt to add NousResearch/hermes-agent,
add it as ``upstream``, or fetch it — including when a maintainer already
added that remote (docs/forking.md). ``hermes update --check`` compares
origin, not that remote.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import hermes_cli.main as cli_main
import hermes_cli.update_custody as custody
from hermes_cli import update_cmd, update_cmd_check

_HERMES = "https://github.com/NousResearch/hermes-agent.git"
_LINA_URLS = (
    "https://github.com/lina-system/lina.git",
    "https://github.com/lina-system/lina",
    "git@github.com:lina-system/lina.git",
    "ssh://git@github.com/lina-system/lina.git",
)


def _git(cwd: Path, *args: str) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["git", *args], cwd=cwd, capture_output=True, text=True,
        encoding="utf-8", errors="replace", check=False,
    )
    if result.returncode != 0:
        raise AssertionError(f"git {args} -> {result.returncode}\n{result.stderr}")
    return result


def _checkout(tmp_path: Path) -> Path:
    origin = tmp_path / "origin"
    origin.mkdir()
    _git(origin, "init", "-q", "-b", "main")
    (origin / "README").write_text("lina\n", encoding="utf-8")
    _git(origin, "add", "README")
    _git(origin, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-qm", "init")
    checkout = tmp_path / "checkout"
    _git(tmp_path, "clone", "-q", origin.as_uri(), str(checkout))
    # ``git remote get-url`` applies insteadOf, so the updater must see the
    # product URL itself. A dead proxy makes any fetch that still targets
    # github.com fail immediately.
    _git(checkout, "config", "http.https://github.com.proxy", "http://127.0.0.1:9")
    _git(checkout, "remote", "set-url", "origin", _LINA_URLS[0])
    return checkout


def test_lina_origin_preflight_does_not_prompt_or_fetch_hermes(tmp_path, monkeypatch, capsys):
    for key, value in {
        "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
        "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
    }.items():
        monkeypatch.setenv(key, value)

    checkout = _checkout(tmp_path)
    local_origin = (tmp_path / "origin").resolve().as_uri()
    monkeypatch.setattr(cli_main, "PROJECT_ROOT", checkout)
    prompts: list[str] = []
    forbidden: list[list[str]] = []
    real_run_git = custody.run_git

    def answer(prompt, default=""):
        prompts.append(prompt)
        return "y"

    def guarding_run_git(git_cmd, args, **kwargs):
        argv = [str(part) for part in args]
        text = " ".join(argv)
        if (
            "hermes-agent" in text or "NousResearch" in text
            or (argv and argv[0] == "fetch" and "upstream" in argv)
            or argv[:3] == ["remote", "add", "upstream"]
        ):
            forbidden.append(argv)
            return subprocess.CompletedProcess(git_cmd + argv, 1, "", "blocked")
        # An origin fetch is the allowed update. Satisfy it from the local
        # stand-in; the configured URL stays the product repository.
        if argv and argv[0] == "fetch" and "origin" in argv:
            argv = [local_origin if part == "origin" else part for part in argv]
        return real_run_git(git_cmd, argv, **kwargs)

    monkeypatch.setattr(custody, "run_git", guarding_run_git)

    def preflight():
        origin_url = update_cmd._get_origin_url(["git"], checkout)
        return update_cmd._prepare_checkout_for_update(
            ["git"], "main", "main",
            is_fork=update_cmd._is_fork(origin_url),
            assume_yes=False, gateway_mode=False, gw_input_fn=answer,
            switch_branch=False, _windows_gateway_resume=None,
        )

    for url in _LINA_URLS:
        _git(checkout, "remote", "set-url", "origin", url)
        plan = preflight()
        assert plan.commit_count == 0
        assert plan.upstream_checked is True
        assert _git(checkout, "remote").stdout.split() == ["origin"]

    _git(checkout, "remote", "set-url", "origin", _LINA_URLS[0])
    # Maintainer remote from docs/forking.md. The test adds it; the update must not fetch it.
    subprocess.run(
        ["git", "remote", "add", "upstream", _HERMES], cwd=checkout, check=True,
    )
    plan = preflight()
    assert plan.commit_count == 0
    assert plan.upstream_checked is True

    fetch_result, compare_branch = update_cmd_check.fetch_compare_branch(
        ["git"], checkout, "main", [],
    )
    assert fetch_result.returncode == 0, fetch_result.stderr
    assert compare_branch == "origin/main"
    local_tip = _git(tmp_path / "origin", "rev-parse", "HEAD").stdout.strip()
    assert _git(checkout, "rev-parse", "origin/main").stdout.strip() == local_tip
    assert _git(checkout, "remote", "get-url", "upstream").stdout.strip() == _HERMES

    out = capsys.readouterr().out
    assert prompts == []
    assert forbidden == []
    assert "official Hermes" not in out
    assert "NousResearch/hermes-agent" not in out
    assert "Fetching from upstream" not in out
