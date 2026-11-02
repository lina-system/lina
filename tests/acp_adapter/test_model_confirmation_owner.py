"""ACP collects guarded model confirmation from the owner."""
from types import SimpleNamespace
import pytest
from hermes_cli.gateway_client import GatewayClientError
from tests.gateway.test_model_confirmation_clients import _Owner

@pytest.mark.asyncio
async def test_acp_set_session_model_asks_through_request_permission(tmp_path, monkeypatch):
    from acp.schema import AllowedOutcome, DeniedOutcome
    from acp_adapter.gateway_server import GatewayACPAgent
    owner = _Owner(tmp_path, monkeypatch)
    asked = []

    class Editor:
        def __init__(self, outcome):
            self.outcome = outcome

        async def request_permission(self, session_id, tool_call, options):
            asked.append((session_id, [o.option_id for o in options], tool_call))
            return SimpleNamespace(outcome=self.outcome)

    agent = GatewayACPAgent()
    agent._gateway = owner
    agent._client = lambda: _ready(owner)
    try:
        before = owner.state()
        agent._conn = Editor(DeniedOutcome(outcome='cancelled'))
        with pytest.raises(GatewayClientError, match='model_switch_cancelled'):
            await agent.set_session_model(model_id='pricey', session_id=owner.sid)
        assert owner.state() == before and [p.get('confirm') for p in owner.mutations] == [None]
        assert asked[0][0] == owner.sid and asked[0][1] == ['allow_once', 'deny']

        owner.mutations.clear()
        agent._conn = Editor(AllowedOutcome(outcome='selected', option_id='allow_once'))
        await agent.set_session_model(model_id='pricey', session_id=owner.sid)
        token = owner.mutations[1].get('confirm')
        assert token and [p.get('confirm') for p in owner.mutations] == [None, token]
        assert owner.state() == ('pricey', before[1] + 1, before[2] + 1)
    finally:
        await owner.close()


async def _ready(value):
    return value
