"""Teams identity uses the credential scope."""
import pytest
from gateway.config import PlatformConfig
from tests.gateway._plugin_adapter_loader import load_plugin_adapter
from tests.gateway.test_multiplex_endpoint_identity_scope import secondary_scope, SECONDARY

pytestmark = pytest.mark.usefixtures("secondary_scope")

def test_teams_credentials_pair_scoped_secret_with_scoped_app_identity():
    teams = load_plugin_adapter("teams")
    assert teams._credentials(PlatformConfig(enabled=True)) == (
        SECONDARY["TEAMS_CLIENT_ID"], SECONDARY["TEAMS_CLIENT_SECRET"], SECONDARY["TEAMS_TENANT_ID"])
    # extra (per-profile config.yaml) must win over any env value.
    assert teams._credentials(PlatformConfig(enabled=True, extra={"client_id": "yaml-id"}))[0] == "yaml-id"


def test_teams_env_enablement_seeds_scoped_identity_and_home_channel():
    teams = load_plugin_adapter("teams")
    seed = teams._env_enablement()
    assert seed["client_id"] == SECONDARY["TEAMS_CLIENT_ID"]
    assert seed["home_channel"]["chat_id"] == SECONDARY["TEAMS_HOME_CHANNEL"]
