"""Teams card gates use only their profile scope."""
from types import SimpleNamespace
import pytest
from tests.gateway.test_adapter_authz_secret_scope import default_env, _scope, _DEFAULT_ENV

pytestmark = pytest.mark.usefixtures("default_env")

_GATES = [
    ("teams.card_action", {"TEAMS_ALLOWED_USERS": "clicker"},
     lambda: __import__("plugins.platforms.teams.adapter", fromlist=["TeamsAdapter"]).TeamsAdapter._card_action_denied(
         SimpleNamespace(aad_object_id="clicker")) is None,
     False, True),
]

@pytest.mark.parametrize("label,scope_opt_in,gate,closed,opened", _GATES, ids=[g[0] for g in _GATES])
def test_default_env_never_answers_a_scoped_gate(label, scope_opt_in, gate, closed, opened):
    """Secondary scope with NO opt-in: the default profile's permissive os.environ must not open it."""
    with _scope({}):
        assert gate() == closed


@pytest.mark.parametrize("label,scope_opt_in,gate,closed,opened", _GATES, ids=[g[0] for g in _GATES])
def test_scope_opt_in_opens_the_gate(monkeypatch, label, scope_opt_in, gate, closed, opened):
    """The secondary's own .env decides — even when the default profile's env is silent."""
    for name in _DEFAULT_ENV:
        monkeypatch.delenv(name, raising=False)
    with _scope(scope_opt_in):
        assert gate() == opened
