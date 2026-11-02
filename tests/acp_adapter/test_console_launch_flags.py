"""The adapter console entry carries launch policy without the kept CLI command."""
from acp_adapter.cli import apply_launch_flags
from acp_adapter.entry import _parse_args


def test_console_flags_reach_launch_environment(monkeypatch):
    names = ('HERMES_MODEL', 'HERMES_INFERENCE_PROVIDER', 'HERMES_SAFE_MODE',
             'HERMES_IGNORE_USER_CONFIG', 'HERMES_ACCEPT_HOOKS', 'HERMES_IGNORE_RULES',
             'HERMES_YOLO_MODE')
    for name in names:
        monkeypatch.delenv(name, raising=False)
    args = _parse_args(['-m', 'fixture', '--provider', 'custom', '--safe-mode',
                        '--accept-hooks', '--ignore-rules', '--yolo'])

    apply_launch_flags(args)

    import os
    assert {name: os.environ[name] for name in names} == dict(zip(names, (
        'fixture', 'custom', '1', '1', '1', '1', '1')))
