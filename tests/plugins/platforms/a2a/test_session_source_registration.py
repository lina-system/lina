"""Source hooks follow the owning platform's registration, not past imports."""
import pytest


def test_local_policy_is_unchanged_without_a_registered_source(tmp_path, monkeypatch):
    from gateway.platform_registry import platform_registry
    from gateway.session_ingress import row_turn_author
    from gateway.session_policy import build_policy, restore_policy
    from dataclasses import asdict

    monkeypatch.setattr(platform_registry, 'get', lambda name: None)
    params = {'cwd': str(tmp_path), 'model': 'fixture'}
    policy = build_policy(params, {'platform_toolsets': {'cli': []}})
    assert restore_policy(asdict(policy)) == policy
    assert row_turn_author(policy, {'payload': {}}) is None


@pytest.mark.asyncio
async def test_unregistered_source_does_not_serve_forwarding_rpc(tmp_path, monkeypatch):
    from types import SimpleNamespace
    from gateway.platform_registry import platform_registry
    from gateway.session_authority import SessionAuthority
    from gateway.session_controls import AuthorityConnection
    from hermes_state import SessionDB
    from hermes_state_runtime import begin_runtime_epoch

    monkeypatch.setattr(platform_registry, 'get', lambda name: None)
    with SessionDB(db_path=tmp_path / 'state.db') as db:
        epoch = begin_runtime_epoch(db, instance_id='fixture')
        authority = SessionAuthority(SimpleNamespace(), profile_id='fixture', instance_id='fixture', db=db, epoch=epoch)
        connection = AuthorityConnection(authority, SimpleNamespace(), {'user_id': 'producer'})
        params = dict(agent='fixture', tenant='', peer='peer', context_id='context', input_id='input', text='hello')
        reply = await connection.dispatch({'id': 1, 'method': 'a2a.forward', 'params': params})
        assert reply['error']['code'] == -32601
