"""Image generation cache paths follow the routed profile's terminal scope."""

from tests.tools import test_multiplex_turn_parity as _parity

two_homes = _parity.two_homes


def test_cache_base_follows_routed_profile(two_homes):
    from gateway.run import _profile_runtime_scope
    from tools import image_generation_tool

    a, b = two_homes
    with _profile_runtime_scope(a):
        assert image_generation_tool._agent_cache_base_for_env(None) is None
    with _profile_runtime_scope(b):
        assert image_generation_tool._agent_cache_base_for_env(None) == "/root/.hermes"
