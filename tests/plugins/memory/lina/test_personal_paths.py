"""lina#308: a relative document path a tool call names is found where the tool reads it. A terminal command's
``workdir`` and the task directory ``tools/file_tools_paths.py`` resolves relative ``read_file`` paths against are
where the ``.lina-personal`` folder is looked for, not this process's directory. The marks are the real store of
tests/_fixtures/memory_store.py; every file is synthetic."""

from __future__ import annotations

from pathlib import Path

import pytest

from agent import personal_work
from plugins.memory.lina import personal

SESSION = "paths-personal"
TASK = "paths-task"


@pytest.fixture
def project(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "home"))
    personal_work._MARKS.clear()
    project = tmp_path / "project"
    (project / "private").mkdir(parents=True)
    (project / "private" / personal_work.MARKER_FILE).write_text("", encoding="utf-8")
    (project / "private" / "id.txt").write_text("Synthetic identity card scan\n", encoding="utf-8")
    elsewhere = tmp_path / "elsewhere"
    elsewhere.mkdir()
    monkeypatch.chdir(elsewhere)
    return project


def _marks() -> list[str]:
    return personal_work.marks_for_home().marked_paths(SESSION)


def test_a_terminal_command_marks_a_relative_document_under_its_workdir(project):
    assert personal.pre_tool_call("terminal", {"command": "cat private/id.txt", "workdir": str(project)},
                                  session_id=SESSION, task_id=TASK) is None

    assert _marks() == [str((project / "private" / "id.txt").resolve())]


def test_a_relative_read_file_path_is_marked_under_the_task_directory(project):
    from tools.terminal_tool import clear_session_cwd, record_session_cwd
    record_session_cwd(TASK, str(project))
    try:
        assert personal.pre_tool_call("read_file", {"path": "private/id.txt"}, session_id=SESSION,
                                      task_id=TASK) is None
    finally:
        clear_session_cwd(TASK)

    assert _marks() == [str((project / "private" / "id.txt").resolve())]


def test_a_relative_path_missing_under_the_workdir_marks_nothing(project, tmp_path):
    (Path.cwd() / "private").mkdir()
    (Path.cwd() / "private" / personal_work.MARKER_FILE).write_text("", encoding="utf-8")
    (Path.cwd() / "private" / "tax.txt").write_text("Synthetic tax return\n", encoding="utf-8")

    assert personal.pre_tool_call("terminal", {"command": "cat private/tax.txt", "workdir": str(project)},
                                  session_id=SESSION, task_id=TASK) is None

    assert _marks() == []
