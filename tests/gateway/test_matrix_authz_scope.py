"""Matrix gates use only their profile scope."""
import pytest
from gateway.config import PlatformConfig
from tests.gateway.test_adapter_authz_secret_scope import default_env, _scope, _DEFAULT_ENV

pytestmark = pytest.mark.usefixtures("default_env")

def _matrix(extra=None):
    from plugins.platforms.matrix.adapter import MatrixAdapter

    return MatrixAdapter(PlatformConfig(enabled=True, token="t", extra={"homeserver": "https://m.example",
                                                                       "user_id": "@bot:m.example", **(extra or {})}))


_GATES = [
    ("matrix.authorized_user", {"MATRIX_ALLOWED_USERS": "@bot2-admin:example.org"},
     lambda: _matrix()._is_authorized_user("@bot2-admin:example.org"),
     False, True),
    ("matrix.ignored_patterns", {"MATRIX_IGNORE_USER_PATTERNS": r"^@bot2-spam:.*"},
     lambda: [p.pattern for p in _matrix()._ignored_user_patterns],
     [], [r"^@bot2-spam:.*"]),
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
