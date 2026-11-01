"""Docker egress preserves operator NODE_OPTIONS."""
import os
import pytest
from agent.proxy_sources import iron_proxy as ip

@pytest.fixture
def hermes_home(tmp_path, monkeypatch):
    """Point HERMES_HOME at a temp dir so install paths don't touch the real $HOME."""

    home = tmp_path / "hermes"
    home.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(home))
    # Make sure no stale provider keys influence discovery.
    for key in list(os.environ):
        if key.endswith("_API_KEY"):
            monkeypatch.delenv(key, raising=False)
    return home


def _sample_mapping(env_name: str = "OPENROUTER_API_KEY") -> ip.TokenMapping:
    return ip.TokenMapping(
        proxy_token=ip.mint_proxy_token("test"),
        real_env_name=env_name,
        upstream_hosts=("openrouter.ai", "*.openrouter.ai"),
    )


def test_docker_egress_node_options_uses_sentinel(hermes_home, monkeypatch):
    """``_egress_proxy_args_for_docker`` should NOT put NODE_OPTIONS in
    env_overrides directly; it uses a sentinel key
    ``_HERMES_EGRESS_NODE_OPTIONS_APPEND`` so DockerEnvironment can
    append-merge with the operator's existing NODE_OPTIONS."""

    from tools.environments.docker import _egress_proxy_args_for_docker
    from hermes_cli.config import load_config, save_config

    state = ip._proxy_state_dir()
    ca = state / "ca.crt"
    ca.write_text("fake-ca")
    (state / "ca.key").write_text("fake-key")
    mapping = _sample_mapping("OPENROUTER_API_KEY")
    proxy_cfg = ip.build_proxy_config(
        mappings=[mapping], ca_cert=ca, ca_key=state / "ca.key", tunnel_port=9090,
    )
    ip.write_proxy_config(proxy_cfg)
    ip.write_mappings([mapping])

    cfg = load_config()
    cfg.setdefault("proxy", {})["enabled"] = True
    cfg["proxy"]["enforce_on_docker"] = True
    save_config(cfg)

    (state / "iron-proxy.pid").write_text("99999")
    monkeypatch.setattr(ip, "_pid_alive", lambda pid: True)
    monkeypatch.setattr(ip, "_port_listening", lambda h, p: True)

    _, env, _ = _egress_proxy_args_for_docker()
    # The egress dict should contain the sentinel, NOT a raw NODE_OPTIONS.
    assert env.get("_HERMES_EGRESS_NODE_OPTIONS_APPEND") == "--use-openssl-ca"
    assert "NODE_OPTIONS" not in env, (
        "NODE_OPTIONS in egress env_overrides would clobber the operator's "
        "docker_env NODE_OPTIONS."
    )
