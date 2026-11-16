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


@pytest.fixture
def bare(tmp_path):
    """A marked folder and an unmarked one, each holding an extensionless document: the task's directory is set to
    the one a test names."""
    from tools.terminal_tool import clear_session_cwd, record_session_cwd
    folders = {}
    for name, marked in (("marked", True), ("unmarked", False)):
        folder = tmp_path / name
        folder.mkdir()
        if marked:
            (folder / personal_work.MARKER_FILE).write_text("", encoding="utf-8")
        (folder / "passport").write_text("Synthetic passport scan\n", encoding="utf-8")
        folders[name] = folder
    yield lambda name: record_session_cwd(TASK, str(folders[name])) or folders[name]
    clear_session_cwd(TASK)


@pytest.mark.parametrize("tool_name, args", [("read_file", {"path": "passport"}),
                                             ("terminal", {"command": "cat passport"})])
def test_an_extensionless_document_in_a_marked_folder_marks_the_job(project, bare, tool_name, args):
    folder = bare("marked")

    assert personal.pre_tool_call(tool_name, args, session_id=SESSION, task_id=TASK) is None

    assert _marks() == [str((folder / "passport").resolve())]


@pytest.mark.parametrize("folder, args", [("marked", {"command": "echo hello passport-free words"}),
                                          ("unmarked", {"command": "cat passport"})])
def test_a_word_that_is_no_file_or_a_file_in_an_unmarked_folder_marks_nothing(project, bare, folder, args):
    bare(folder)

    assert personal.pre_tool_call("terminal", args, session_id=SESSION, task_id=TASK) is None

    assert _marks() == []


@pytest.mark.parametrize("command", ["cat private/id.txt\necho done",
                                     "echo start\nhead -n 3 private/id.txt | wc -l\necho done",
                                     "echo start && ls\ncat \\\n  private/id.txt"],
                         ids=["first line", "middle line", "last line, continued"])
def test_a_multiline_command_marks_the_document_on_any_of_its_lines(project, command):
    assert personal.pre_tool_call("terminal", {"command": command, "workdir": str(project)}, session_id=SESSION,
                                  task_id=TASK) is None

    assert _marks() == [str((project / "private" / "id.txt").resolve())]


def test_a_heredoc_of_plain_prose_marks_nothing(project):
    command = "cat > notes.md <<'EOF'\nIt's a short note: nothing to read here\nEOF"

    assert personal.pre_tool_call("terminal", {"command": command, "workdir": str(project)}, session_id=SESSION,
                                  task_id=TASK) is None

    assert _marks() == []


def test_a_multiline_command_naming_a_file_in_an_unmarked_folder_marks_nothing(project, bare):
    bare("unmarked")

    assert personal.pre_tool_call("terminal", {"command": "echo start\ncat passport\necho done"},
                                  session_id=SESSION, task_id=TASK) is None

    assert _marks() == []


def test_a_whole_filename_with_spaces_still_marks_the_job(project):
    (project / "private" / "scan of id.txt").write_text("Synthetic identity card scan\n", encoding="utf-8")

    assert personal.pre_tool_call("read_file", {"path": str(project / "private" / "scan of id.txt")},
                                  session_id=SESSION, task_id=TASK) is None

    assert _marks() == [str((project / "private" / "scan of id.txt").resolve())]


def test_a_public_post_on_a_later_line_of_a_command_is_refused(project):
    command = f"cd {project}\ngh issue create --title notes --body-file private/id.txt"

    refused = personal.pre_tool_call("terminal", {"command": command, "workdir": str(project)},
                                     session_id=SESSION, task_id=TASK)

    assert refused is not None and refused["action"] == "block" and "public: gh issue create" in refused["message"]


@pytest.mark.parametrize("command", ["cd private && cat id.txt", "cd -- private\ncat id.txt", "pushd private\ncat id.txt",
                                     "cd . && cd private; head -n 3 id.txt | wc -l", "cd missing ; cat private/id.txt"],
                         ids=["and", "option and newline", "pushd", "two cd", "cd to a missing folder, then a path"])
def test_a_document_read_after_a_cd_marks_the_job(project, command):
    assert personal.pre_tool_call("terminal", {"command": command, "workdir": str(project)}, session_id=SESSION,
                                  task_id=TASK) is None

    assert _marks() == [str((project / "private" / "id.txt").resolve())]


@pytest.mark.parametrize("command", ["cat id.txt", "cd . && cat id.txt", "cd private\ncd ..\ncat id.txt",
                                     "cd\ncat id.txt"],
                         ids=["no cd", "cd to the same folder", "cd and back", "bare cd"])
def test_a_document_name_that_is_not_in_the_directory_the_command_runs_in_marks_nothing(project, command):
    assert personal.pre_tool_call("terminal", {"command": command, "workdir": str(project)}, session_id=SESSION,
                                  task_id=TASK) is None

    assert _marks() == []
