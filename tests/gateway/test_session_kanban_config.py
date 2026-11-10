"""lina#346's config keys reach the card session through the loaders the gateway uses.

Each key is written to a temporary config.yaml (hermes_cli/AGENTS.md, config system) and observed
through the consuming surface: ``kanban.goal_context_bound_tokens`` through the owner's goal loop
(``run_worker_turns``, which reads it with ``goal_context_bound()``), and the ``kanban.worker_*`` keys
through ``run_task``, which loads the profile's config.yaml with ``_load_gateway_config``.
"""
import asyncio
import json
import os
from contextlib import closing
from dataclasses import dataclass
from types import SimpleNamespace

from gateway import session_kanban
from hermes_cli import kanban_db as kb
from hermes_cli.kanban_db_connect import connect, connect_closing


class _GoalAgent:
    """A goal-mode card session whose context stays at ``tokens`` after every turn."""
    session_id = "s1"

    def __init__(self, tokens):
        self.calls = 0
        self.context_compressor = SimpleNamespace(last_prompt_tokens=tokens)
        self._session_db = self

    def get_messages_as_conversation(self, _sid):
        return []

    def run_conversation(self, message, conversation_history=None, turn_author=None):
        self.calls += 1
        return {"completed": True, "final_response": "still working", "messages": []}


def _goal_frame(tmp_path, monkeypatch, config_yaml):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    for key in ("HERMES_KANBAN_TASK", "HERMES_KANBAN_DB", "HERMES_KANBAN_RUN_ID", "HERMES_KANBAN_CLAIM_LOCK"):
        monkeypatch.delenv(key, raising=False)
    (tmp_path / "config.yaml").write_text(config_yaml, encoding="utf-8")
    monkeypatch.setattr("agent.skill_commands.build_preloaded_skills_prompt", lambda *a, **k: ("", [], []))
    monkeypatch.setattr("hermes_cli.goals.judge_goal", lambda *a, **k: ("continue", "not yet", False, None, False))
    kb.init_db()
    path = kb.kanban_db_path()
    with connect_closing(path) as conn:
        task_id = kb.create_task(conn, title="goal", assignee="default", goal_mode=True)
        task = kb.claim_task(conn, task_id, claimer="owner")
        with kb.write_txn(conn):
            kb._append_event(conn, task_id, "worker_bound", {"pid": os.getpid(), "claim_lock": task.claim_lock},
                             run_id=task.current_run_id)
    context = {"db": str(path), "task_id": task_id, "run_id": task.current_run_id, "claim_lock": task.claim_lock,
               "skills": [], "context": "", "goal_mode": True, "goal_text": "goal", "goal_max_turns": 3}
    return path, task_id, {"text": "work", "policy": {"kanban_json": json.dumps(context)}}


def _handoff_kind(path, task_id):
    with connect_closing(path) as conn:
        parked = [e for e in kb.list_events(conn, task_id) if e.kind == "scheduled"]
    return parked[-1].payload["wait_for"]["handoff"] if parked else None


def test_a_profile_bound_below_the_session_context_hands_off_after_one_turn(tmp_path, monkeypatch):
    path, task_id, frame = _goal_frame(tmp_path, monkeypatch, "kanban:\n  goal_context_bound_tokens: 5000\n")
    agent = _GoalAgent(tokens=6000)  # under the 200000 default, over the configured bound

    session_kanban.run_worker_turns(agent, frame, [])

    assert agent.calls == 1
    assert _handoff_kind(path, task_id) == "context_bound"


def test_a_profile_bound_of_zero_never_hands_off_on_context(tmp_path, monkeypatch):
    path, task_id, frame = _goal_frame(tmp_path, monkeypatch, "kanban:\n  goal_context_bound_tokens: 0\n")
    agent = _GoalAgent(tokens=900_000)  # over the 200000 default

    session_kanban.run_worker_turns(agent, frame, [])

    assert agent.calls == 3  # the whole turn budget ran
    assert _handoff_kind(path, task_id) == "turn_budget"


@dataclass
class _Receipt:
    status: str


def test_profile_worker_keys_reach_the_card_session_through_the_gateway_loader(tmp_path, monkeypatch):
    """The profile's config.yaml (read by ``run_task`` through ``_load_gateway_config``) beats the
    deployment root's for the model and provider; the effort it leaves unset comes from the root."""
    from gateway.session_contract import Principal
    root, profile = tmp_path / "root", tmp_path / "root" / "profiles" / "elias"
    profile.mkdir(parents=True)
    (root / "config.yaml").write_text(
        "kanban:\n  worker_model: deploy-model\n  worker_reasoning_effort: high\n", encoding="utf-8")
    (profile / "config.yaml").write_text(
        "model:\n  default: agent-model\n  provider: custom\nplatform_toolsets:\n  cli: [terminal]\n"
        "kanban:\n  worker_model: profile-model\n  worker_provider: custom\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(profile))
    monkeypatch.setenv("HERMES_KANBAN_HOME", str(tmp_path / "kanban"))
    monkeypatch.setenv("HOME", str(tmp_path / "home"))
    monkeypatch.delenv("HERMES_KANBAN_DB", raising=False)
    monkeypatch.delenv("HERMES_DELEGATED_CHILD", raising=False)
    monkeypatch.setattr("gateway.run._hermes_home", profile)
    monkeypatch.setattr("hermes_cli.profiles.resolve_profile_env", lambda name: str(profile))
    monkeypatch.setattr("gateway.session_local.create_local_session",
                        lambda *a, **k: SimpleNamespace(session_id="owner-session", profile_id=str(profile)))
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    kb.create_board("owned")
    with closing(connect(board="owned")) as conn:
        task_id = kb.create_task(conn, title="goal", assignee="elias", goal_mode=True,
                                 workspace_kind="dir", workspace_path=str(workspace))
        kb.recompute_ready(conn)
        task = kb.claim_task(conn, task_id)

    async def submit(actor, submission, **kwargs):
        return _Receipt("queued")

    async def resume(ref, params):
        return None
    db = SimpleNamespace(get_session=lambda sid: None, db_path=str(profile / "state.db"))
    actor = Principal("owner", str(profile), frozenset({"session:create"}), "transport")
    connection = SimpleNamespace(authority=SimpleNamespace(profile_id=str(profile), db=db, submit=submit),
                                 actor=actor, native_owner=True, resume=resume)
    params = dict(board="owned", task_id=task.id, run_id=task.current_run_id, claim_lock=task.claim_lock)

    asyncio.run(session_kanban.run_task(connection, params))

    with closing(connect(board="owned")) as conn:
        shown = [e.payload for e in kb.list_events(conn, task.id) if e.kind == "session_model"]
    assert [(s["model"], s["provider"], s["reasoning"]) for s in shown] == [("profile-model", "custom", "high")]
