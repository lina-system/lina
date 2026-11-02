"""Modal snapshots follow the bound profile."""
from tests.tools.test_multiplex_tool_memo_scope import _under
from tools.environments import modal
pytest_plugins = ["tests.tools.test_multiplex_tool_memo_scope"]

def test_modal_snapshot_path_uses_bound_profile(two_profiles):
    prof_a, prof_b = two_profiles
    assert _under(prof_a, modal._snapshot_store) == prof_a / "modal_snapshots.json"
    assert _under(prof_b, modal._snapshot_store) == prof_b / "modal_snapshots.json"
