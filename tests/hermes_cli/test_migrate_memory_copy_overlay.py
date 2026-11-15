"""A migrated agent asked "what is in progress" is sent to its own work record (lina#719): ``lina migrate
memory`` writes the copy's work-status instruction after its persona on every channel, its terminal resolves
``lina`` to the copy's install ahead of an old wrapper, and the report lists the instruction and what the copied
files still say about the previous machine. Deterministic: the prompt and the terminal are built, no model runs."""

from __future__ import annotations

import json
import os
import subprocess
import sys
from types import SimpleNamespace

import pytest

from tests._fixtures.copy_install import login_profile_puts_first, old_wrapper, terminal_resolves, venv_install
from tests.hermes_cli.migrate_memory_fixtures import legacy_home

OLD_HOSTS = ("old-hub", "old-node")
OLD_LINES = {
    "SOUL.md": "Work status is computed from the kanban board.\n",
    "memories/MEMORY.md": "The kanban board runs on old-hub; old-node runs its workers.\n",
    "skills/example/SKILL.md": 'K="hermes -p default kanban --board work"\nssh old-hub "$K list"\n'
                               "session-queue on old-node\n",
}


def _migrate(tmp_path, store, install):
    source = legacy_home(tmp_path / "hermes")
    for relative, line in OLD_LINES.items():
        path = source / "profiles/alpha" / relative
        path.write_text(path.read_text() + line)
    args = ["migrate", "memory", "--from", str(source), "--source-profile", "alpha", "--as", "alpha-copy",
            "--home", str(tmp_path / "lina"), "--work-dir", str(tmp_path / "copy"),
            *(arg for host in OLD_HOSTS for arg in ("--old-host", host))]
    script = ("import sys; import hermes_state_store as s; dsn=sys.argv.pop(1); "
              "s.resolve_owner_dsn=lambda:dsn; from hermes_cli.main import main; main()")
    # The install's own interpreter runs the command, as the installed `lina` does.
    done = subprocess.run([str(install / "python"), "-c", script, store.superuser_dsn(), *args],
                          capture_output=True, text=True, timeout=90, check=False,
                          env={**os.environ, "LINA_HOME": str(tmp_path / "lina"),
                               "HERMES_HOME": str(tmp_path / "lina")})
    assert done.returncode == 0, done.stdout + done.stderr
    return source / "profiles/alpha", tmp_path / "lina/profiles/alpha-copy", json.loads(done.stdout)


def _prompt(copy, platform: str) -> str:
    from agent.system_prompt import build_system_prompt

    agent = SimpleNamespace(
        load_soul_identity=True, skip_context_files=True, valid_tool_names=[], _task_completion_guidance=False,
        _tool_use_enforcement=False, _environment_probe=False, _kanban_worker_guidance="", _memory_store=None,
        _memory_manager=None, model="", provider="", platform=platform, pass_session_id=False, session_id="",
        _session_db=SimpleNamespace(db_path=str(copy / "state.db")), _emit_status=lambda *_a, **_k: None)
    return build_system_prompt(agent)


@pytest.mark.platforms("posix")
def test_migrated_copy_answers_status_from_its_own_work_record(tmp_path, _memory_store, monkeypatch):
    from hermes_cli.config_backend import FileBackend
    from tools.environments import local

    install = venv_install(tmp_path / "install")
    source, copy, report = _migrate(tmp_path, _memory_store, install)

    # The copied files stay byte-identical to the source; nothing in them is rewritten.
    for relative in OLD_LINES:
        assert (copy / relative).read_bytes() == (source / relative).read_bytes()

    # (c) The report lists the instruction it wrote and, read-only, what the copied files still say.
    assert report["ok"] and "SOUL.overlay.md" in report["files"]
    assert report["overlay"] == {"file": "SOUL.overlay.md", "lina": str(install / "lina"),
                                 "old_hosts": list(OLD_HOSTS)}
    census = report["census"]
    assert census["old_work_command"] == {"files": 1, "hits": 2,
                                          "top": [{"file": "skills/example/SKILL.md", "hits": 2}]}
    assert census["ssh_to_old_host"] == {"files": 1, "hits": 1,
                                         "top": [{"file": "skills/example/SKILL.md", "hits": 1}]}
    assert census["old_host_name"]["files"] == 2 and census["old_host_name"]["hits"] == 4
    assert "rewritten" not in json.dumps(report)

    # (a) On every channel the prompt sends status questions to the copy's own record, after the persona.
    lina = f"{install / 'lina'} work"
    for platform in ("cli", "telegram", "api_server"):
        prompt = _prompt(copy, platform)
        persona, overlay = prompt.index(OLD_LINES["SOUL.md"].strip()), prompt.index(lina)
        assert persona < overlay, platform
        instruction = prompt[overlay:]
        for off_limits in ("hermes kanban", "session-queue", *OLD_HOSTS):
            assert off_limits in instruction, (platform, off_limits)

    # (b) The copy's terminal, run with the copy's config as the CLI bridges it, resolves `lina` and `hermes`
    # to its own install, ahead of the old wrapper the process PATH and a login profile put first.
    wrapper = old_wrapper(tmp_path / "user/.local/bin")
    for home in (tmp_path / "user", copy / "home"):
        login_profile_puts_first(home, wrapper)
    monkeypatch.setattr(sys, "executable", str(install / "python"))
    monkeypatch.setenv("HOME", str(tmp_path / "user"))
    monkeypatch.setenv("HERMES_HOME", str(copy))
    monkeypatch.setenv("TERMINAL_HOME_MODE", FileBackend().read_user_doc(copy)["terminal"]["home_mode"])
    monkeypatch.setenv("PATH", f"{wrapper}:/usr/local/bin:/usr/bin:/bin")
    monkeypatch.setattr(local, "_HERMES_BIN_DIR", local._SENTINEL)
    assert terminal_resolves(copy, "lina", "hermes") == [str(install / "lina"), str(install / "hermes")]
