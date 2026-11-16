"""lina#308 through the real agent and lina-memory: the owner marks a document personal (``mark_personal``) or its
folder does (``.lina-personal``); the job's turns are stored with their numbers masked, a public post or third-party
upload of the document is refused at the tool layer while handing it to a worker is not, and the job's work folders
are gone when the session ends. All text and numbers are synthetic."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from tests.plugins.memory.lina.harness import drain, make_agent, run_turn, write_home

SESSION = "personal-1"
NUMBER = "900101-1234567"
MASKED = "******-***4567"


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


@pytest.fixture
def document(tmp_path) -> Path:
    path = tmp_path / "docs" / "contract.txt"
    path.parent.mkdir()
    path.write_text(f"Synthetic lease contract for a test tenant\n주민등록번호 {NUMBER}\n", encoding="utf-8")
    return path


def _provider(agent):
    return next(p for p in agent._memory_manager.providers if p.name == "lina-memory")


def test_a_marked_document_is_masked_in_the_store_and_refused_for_public_posts_but_not_for_workers(
        home, document, memory_store_sql):
    from hermes_cli.plugins import _dispatch_pre_tool_call_hooks
    from hermes_state import SessionDB
    from model_tools import handle_function_call
    db = SessionDB(home / "state.db")
    agent = make_agent(SESSION, db)
    provider = _provider(agent)
    try:
        assert "mark_personal" in {schema["name"] for schema in provider.get_tool_schemas()}
        marked = json.loads(provider.handle_tool_call("mark_personal", {"path": str(document)}))
        work_dir = Path(marked["work_dir"])
        (work_dir / "extract.txt").write_text(document.read_text(encoding="utf-8"), encoding="utf-8")
        run_turn(agent, f"내 등록번호는 {NUMBER}이야", f"{NUMBER}로 확인했어요.")
        drain(agent)

        public = _dispatch_pre_tool_call_hooks(
            "terminal", {"command": f"gh issue create --title lease --body-file {document}"}, session_id=SESSION)
        derived = _dispatch_pre_tool_call_hooks(
            "terminal", {"command": "gh issue create --title lease --body 'Synthetic lease contract for a test tenant'"},
            session_id=SESSION)
        upload = json.loads(handle_function_call(
            "terminal", {"command": f"curl -F file=@{work_dir / 'extract.txt'} http://127.0.0.1:9/convert"},
            session_id=SESSION))
        worker = _dispatch_pre_tool_call_hooks(
            "delegate_task", {"goal": f"Summarize {document} for the owner"}, session_id=SESSION)
        other_job = _dispatch_pre_tool_call_hooks(
            "terminal", {"command": "gh issue create --title notes --body 'meeting notes'"}, session_id=SESSION)
        unmarked_session = _dispatch_pre_tool_call_hooks(
            "terminal", {"command": f"gh issue create --body {NUMBER}"}, session_id="someone-else")
        stored = memory_store_sql("SELECT content FROM memory.messages WHERE session_id = %s ORDER BY message_id",
                                  (SESSION,))
        provider.on_session_end([])
        events = memory_store_sql("SELECT kind, detail FROM memory.personal_job_events ORDER BY event_id")
    finally:
        agent.shutdown_memory_provider()
        db.close()

    assert [content for (content,) in stored] == [f"내 등록번호는 {MASKED}이야", f"{MASKED}로 확인했어요."]
    assert public[0].startswith("Refused (public: gh issue create): it names the personal document")
    assert derived[0].startswith("Refused (public: gh issue create): it carries text from a personal document")
    assert upload["error"].startswith("Refused (third party: curl upload)")
    assert worker == (None, None)
    assert other_job == (None, None)
    assert unmarked_session == (None, None)
    assert not work_dir.exists() and not Path(marked["temp_dir"]).exists()
    kinds = [kind for kind, _ in events]
    assert kinds.count("refused") == 3
    assert [detail["deleted"] for kind, detail in events if kind == "cleaned"] == [1]


class _Compressor:
    """The context engine's ``compress``, answering a fixed summary and retained exchange (no model call)."""

    compression_count = 1
    last_prompt_tokens = last_completion_tokens = 0
    _last_summary_error = None
    _last_compress_aborted = False
    _last_aux_model_failure_model = _last_aux_model_failure_error = None

    def __init__(self, compressed: list[dict]) -> None:
        self._compressed = compressed

    def compress(self, *_args, **_kwargs) -> list[dict]:
        return [dict(message) for message in self._compressed]

    def __getattr__(self, name: str):
        from unittest.mock import MagicMock
        return MagicMock(name=name)


@pytest.mark.parametrize("marked", [True, False])
def test_a_rotated_compaction_child_of_a_marked_job_is_stored_masked(home, document, memory_store_sql, marked):
    from hermes_state import SessionDB
    db = SessionDB(home / "state.db")
    agent = make_agent(SESSION, db)
    try:
        if marked:
            _provider(agent).handle_tool_call("mark_personal", {"path": str(document)})
        run_turn(agent, "계약서 읽어줘", "읽었어요.")
        drain(agent)
        agent.compression_in_place = False
        agent.context_compressor = _Compressor([
            {"role": "user", "content": f"[CONTEXT COMPACTION] The owner gave the number {NUMBER}."},
            {"role": "user", "content": f"내 등록번호는 {NUMBER}이야"},
            {"role": "assistant", "content": f"{NUMBER}로 확인했어요."}])
        agent._compress_context([{"role": "user", "content": f"synthetic page {i} " * 40} for i in range(6)], "sys",
                                approx_tokens=10_000)
        child = agent.session_id
        stored = memory_store_sql("SELECT content FROM memory.messages WHERE session_id = %s ORDER BY message_id",
                                  (child,))
    finally:
        agent.shutdown_memory_provider()
        db.close()

    number = MASKED if marked else NUMBER
    assert child != SESSION
    assert [content for (content,) in stored] == [
        f"[CONTEXT COMPACTION] The owner gave the number {number}.", f"내 등록번호는 {number}이야",
        f"{number}로 확인했어요."]


def test_a_document_read_from_a_marker_folder_is_marked_for_the_job(home, tmp_path, memory_store_sql):
    from hermes_cli.plugins import _dispatch_pre_tool_call_hooks
    from hermes_state import SessionDB
    folder = tmp_path / "scans"
    folder.mkdir()
    (folder / ".lina-personal").write_text("", encoding="utf-8")
    scan = folder / "id-card.txt"
    scan.write_text(f"ID {NUMBER}\n", encoding="utf-8")
    db = SessionDB(home / "state.db")
    agent = make_agent(SESSION, db)
    try:
        assert _dispatch_pre_tool_call_hooks("read_file", {"path": str(scan)}, session_id=SESSION) == (None, None)
        run_turn(agent, f"카드 번호 {NUMBER} 맞아?", "네.")
        drain(agent)
        marks = memory_store_sql("SELECT job_id, path, how FROM memory.personal_marks")
        stored = memory_store_sql("SELECT content FROM memory.messages WHERE session_id = %s AND role = 'user'",
                                  (SESSION,))
    finally:
        agent.shutdown_memory_provider()
        db.close()

    assert marks == [(SESSION, str(scan.resolve()), "folder")]
    assert stored == [(f"카드 번호 {MASKED} 맞아?",)]
