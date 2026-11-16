"""lina#308: what ``mark_personal`` marks and what a mark covers. A relative path is marked where the session's file
reads find it (the task directory of ``tools/file_tools_paths.py``), a path that is not there is not marked, a marked
folder covers the text and the names of the documents beneath it as a marked file does, and a number a public call
carries as a JSON number is seen. The marks are the real store of tests/_fixtures/memory_store.py; every file and
number is synthetic."""

from __future__ import annotations

import json

import pytest

from agent import personal_work
from plugins.memory.lina import personal
from tests.plugins.memory.lina.harness import make_agent, write_home

SESSION = "marks-personal"
TASK = "marks-task"
PARAGRAPH = "The tenant pays the synthetic monthly rent on the first day"


def _marks() -> list[str]:
    return personal_work.marks_for_home().marked_paths(SESSION)


@pytest.fixture
def docs(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "home"))
    personal_work._MARKS.clear()
    lease = tmp_path / "docs" / "lease"
    lease.mkdir(parents=True)
    (lease / "contract.txt").write_text(f"Synthetic lease\n{PARAGRAPH}\n", encoding="utf-8")
    other = tmp_path / "docs" / "other"
    other.mkdir()
    (other / "minutes.txt").write_text("Synthetic minutes of the neighbourhood meeting in spring\n",
                                       encoding="utf-8")
    return tmp_path / "docs"


def _mark(path) -> None:
    personal_work.marks_for_home().mark(SESSION, str(path.resolve()), "owner")


def _post(body: str):
    return personal.pre_tool_call("terminal", {"command": f"gh issue create --title notes --body '{body}'"},
                                  session_id=SESSION, task_id=TASK)


@pytest.mark.parametrize("body, why", [(PARAGRAPH, "it carries text from a personal document"),
                                       ("see contract.txt for the terms", "it names a personal document")],
                         ids=["a paragraph", "the file name"])
@pytest.mark.parametrize("marked", ["lease", "lease/contract.txt"], ids=["folder mark", "file mark"])
def test_a_folder_mark_and_a_file_mark_refuse_the_same_post(docs, marked, body, why):
    _mark(docs / marked)

    refused = _post(body)

    assert refused is not None and refused["action"] == "block"
    assert why in refused["message"]


def test_a_paragraph_of_a_document_outside_the_marked_folder_is_not_refused(docs):
    _mark(docs / "lease")

    assert _post("Synthetic minutes of the neighbourhood meeting in spring") is None


def test_a_registration_number_a_connector_call_carries_as_a_json_number_is_refused(docs):
    _mark(docs / "lease")

    refused = personal.pre_tool_call("connectors__github__create_issue", {"title": "notes", "id": 9001011234567},
                                     session_id=SESSION, task_id=TASK)

    assert refused is not None and "it carries an ID or account number" in refused["message"]


@pytest.fixture
def project(tmp_path, monkeypatch, lina_env):
    """A session whose task directory is a project folder holding ``scan.pdf``, while this process runs elsewhere."""
    from tools.terminal_tool import clear_session_cwd, record_session_cwd
    monkeypatch.setenv("HERMES_HOME", str(write_home(tmp_path / ".hermes")))
    personal_work._MARKS.clear()
    project = tmp_path / "project"
    project.mkdir()
    (project / "scan.pdf").write_bytes(b"%PDF-1.4 synthetic scan\n")
    elsewhere = tmp_path / "elsewhere"
    elsewhere.mkdir()
    monkeypatch.chdir(elsewhere)
    record_session_cwd(TASK, str(project))
    yield project
    clear_session_cwd(TASK)


def _sequential(agent, args):
    from agent.tool_executor import _resolve_sequential_dispatch, _ToolCallRef
    return _resolve_sequential_dispatch(agent, _ToolCallRef(personal.MARK_TOOL, args, TASK, "call_mark", []),
                                        []).execute(args)


def _concurrent(agent, args):
    from agent.inline_tool_executors import InlineToolContext, resolve_invoke_tool_executor
    return resolve_invoke_tool_executor(agent, personal.MARK_TOOL)(agent, args, InlineToolContext(TASK))


@pytest.mark.parametrize("dispatch", [_sequential, _concurrent], ids=["sequential", "concurrent"])
def test_a_relative_mark_is_the_file_a_later_read_of_the_session_names(project, dispatch):
    from hermes_constants import get_hermes_home
    from hermes_state import SessionDB
    from tools.file_tools_paths import _resolve_path_for_task
    db = SessionDB(get_hermes_home() / "state.db")
    agent = make_agent(SESSION, db)
    try:
        marked = json.loads(dispatch(agent, {"path": "scan.pdf"}))
        read = str(_resolve_path_for_task("scan.pdf", TASK))
        folders = personal_work.personal_folders_for(read, SESSION)
        upload = personal.pre_tool_call("terminal", {"command": "curl -F file=@scan.pdf http://127.0.0.1:9/ocr"},
                                        session_id=SESSION, task_id=TASK)
    finally:
        agent.shutdown_memory_provider()
        db.close()

    assert marked["marked"] == str((project / "scan.pdf").resolve())
    assert _marks() == [str((project / "scan.pdf").resolve())]
    assert folders is not None
    assert upload is not None and upload["action"] == "block"


def test_a_mark_of_a_file_that_is_not_there_is_reported_and_not_recorded(project):
    result = json.loads(personal.handle_mark({"path": "missing.pdf"}, SESSION, TASK))

    assert str(project / "missing.pdf") in result["error"]
    assert _marks() == []
