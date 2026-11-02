"""The gateway command parity table is generated from the code and must be regenerated with it."""
import importlib.util
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def _generator():
    spec = importlib.util.spec_from_file_location("gen_gateway_command_parity",
                                                  ROOT / "scripts" / "gen_gateway_command_parity.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_acp_column_is_the_table_acp_dispatch_reads(monkeypatch):
    """Editor route labels have one source shared with ACP dispatch: a route it changes
    changes the documented cell, and a command it drops falls back to the owner's ``slash.exec``."""
    from hermes_cli.gateway_command_routes import EDITOR_COMMAND_ROUTES
    gen = _generator()
    assert gen.acp_cell('undo', 'refused') == EDITOR_COMMAND_ROUTES['undo']
    monkeypatch.setitem(EDITOR_COMMAND_ROUTES, 'undo', 'moved route')
    assert gen.acp_cell('undo', 'refused') == 'moved route'
    monkeypatch.delitem(EDITOR_COMMAND_ROUTES, 'undo')
    assert gen.acp_cell('undo', 'refused') == 'slash.exec: refused'
