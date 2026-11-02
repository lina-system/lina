"""Video provider endpoints and credentials share the active profile scope."""

from tests.agent import test_multiplex_base_url_scope as _scope

secondary_scope = _scope.secondary_scope


def test_xai_scoped_miss_does_not_borrow_launch_credentials(secondary_scope):
    import plugins.video_gen.xai as xai_video

    api_key, base_url = xai_video._resolve_xai_credentials()
    assert api_key == "" and "default.example" not in base_url
