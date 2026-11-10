"""Card sessions run on the deployment's card-session model unless the card names its own (lina#346).

The card shows the model and effort in effect; a review run keeps the agent's own model.
"""
import asyncio
import json
from contextlib import closing
from dataclasses import dataclass
from types import SimpleNamespace

import pytest

START = 'kanban:\n  worker_model: claude-sonnet-5.5\n  worker_reasoning_effort: xhigh\n'
@dataclass
class _Receipt:
    status: str


AGENT_CONFIG = {'model': {'default': 'agent-model', 'provider': 'custom'}, 'platform_toolsets': {'cli': ['terminal']}}


@pytest.fixture
def owner(tmp_path, monkeypatch):
    from hermes_cli import kanban_db as kb
    from gateway.session_contract import Principal
    monkeypatch.setenv('HERMES_KANBAN_HOME', str(tmp_path))
    monkeypatch.setenv('HERMES_HOME', str(tmp_path))
    monkeypatch.setenv('HOME', str(tmp_path / 'home'))
    monkeypatch.delenv('HERMES_KANBAN_DB', raising=False)
    monkeypatch.delenv('HERMES_DELEGATED_CHILD', raising=False)
    monkeypatch.setattr('hermes_cli.profiles.resolve_profile_env', lambda name: str(tmp_path))
    (tmp_path / 'workspace').mkdir()
    kb.create_board('owned')
    actor = Principal('owner', str(tmp_path), frozenset({'session:create'}), 'transport')
    return SimpleNamespace(home=tmp_path, actor=actor, connection=SimpleNamespace(
        authority=SimpleNamespace(profile_id=str(tmp_path)), actor=actor, native_owner=True))


def _claim(owner, **card):
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect
    with closing(connect(board='owned')) as conn:
        task_id = kb.create_task(conn, title='Goal card', body='Ship it', assignee='default', goal_mode=True,
                                 workspace_kind='dir', workspace_path=str(owner.home / 'workspace'), **card)
        kb.recompute_ready(conn)
        task = kb.claim_task(conn, task_id)
    return task, dict(board='owned', task_id=task.id, run_id=task.current_run_id, claim_lock=task.claim_lock)


def _effort(policy):
    reasoning = policy.reasoning_config or {}
    return reasoning.get('effort') if reasoning.get('enabled') else None


@pytest.mark.parametrize('card, profile_kanban, deployment_yaml, model, effort, origin', [
    # The deployment's config.yaml sets it (the start value) for every agent.
    ({}, None, START, 'claude-sonnet-5.5', 'xhigh', ('deployment', 'deployment')),
    # Nothing set anywhere: the agent's own model and effort.
    ({}, None, None, 'agent-model', None, ('agent', 'agent')),
    # The card names its own model and effort: they win.
    ({'model_override': 'card-model', 'provider_override': 'custom', 'reasoning_effort': 'low'}, None,
     'kanban:\n  worker_model: deploy-model\n', 'card-model', 'low', ('card', 'card')),
    # Only the effort on the card: the model still comes from the deployment.
    ({'reasoning_effort': 'low'}, None, START, 'claude-sonnet-5.5', 'low', ('deployment', 'card')),
    # The agent's profile config explicitly opts out: the agent's own model.
    ({}, {'worker_model': '', 'worker_reasoning_effort': ''}, START, 'agent-model', None, ('agent', 'agent')),
])
def test_card_session_model_precedence(owner, card, profile_kanban, deployment_yaml, model, effort, origin):
    from gateway.session_kanban import build_kanban_policy
    if deployment_yaml:
        (owner.home / 'config.yaml').write_text(deployment_yaml)
    _task, params = _claim(owner, **card)
    config = dict(AGENT_CONFIG, **({'kanban': profile_kanban} if profile_kanban is not None else {}))
    policy, _ = build_kanban_policy(owner.connection, params, config)
    assert (policy.model, _effort(policy)) == (model, effort)
    shown = json.loads(policy.kanban_json)['session_model']
    assert (shown['model'], shown['reasoning']) == (model, effort)
    assert (shown['model_from'], shown['reasoning_from']) == origin


def test_review_run_keeps_agent_model(owner, monkeypatch):
    from gateway.session_kanban import build_kanban_policy
    from hermes_cli import kanban_db as kb
    (owner.home / 'config.yaml').write_text(START)
    _task, params = _claim(owner)
    monkeypatch.setattr(kb, '_retry_status_for_run', lambda *a: 'review')
    policy, _ = build_kanban_policy(owner.connection, params, AGENT_CONFIG)
    assert policy.model == 'agent-model'
    assert json.loads(policy.kanban_json)['session_model']['model_from'] == 'agent'


def test_card_shows_session_model_of_its_attempt(owner, monkeypatch):
    """The admitted attempt writes a run-scoped ``session_model`` event, which ``kanban show`` lists."""
    from gateway.session_kanban import run_task
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect
    (owner.home / 'config.yaml').write_text(START)
    task, params = _claim(owner, reasoning_effort='high')
    monkeypatch.setattr('gateway.run._load_gateway_config', lambda: AGENT_CONFIG)
    monkeypatch.setattr('gateway.session_local.create_local_session',
                        lambda *a, **k: SimpleNamespace(session_id='owner-session', profile_id=str(owner.home)))

    async def submit(actor, submission, **kwargs):
        return _Receipt('queued')

    async def resume(ref, params):
        return None
    db = SimpleNamespace(get_session=lambda sid: None, db_path=str(owner.home / 'state.db'))
    owner.connection.authority = SimpleNamespace(profile_id=str(owner.home), db=db, submit=submit)
    owner.connection.resume = resume
    asyncio.run(run_task(owner.connection, params))
    with closing(connect(board='owned')) as conn:
        events = [e for e in kb.list_events(conn, task.id) if e.kind == 'session_model']
    assert [e.run_id for e in events] == [task.current_run_id]
    assert events[0].payload['model'] == 'claude-sonnet-5.5'
    assert events[0].payload['reasoning'] == 'high'
