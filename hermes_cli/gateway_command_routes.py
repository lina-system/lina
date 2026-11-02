"""Editor route descriptions shared by dispatch and gateway parity reporting."""

EDITOR_COMMAND_ROUTES = {
    'branch': 'ACP fork_session',
    'discard': 'prompt.resolve_unknown',
    'help': 'local',
    'steer': 'session.steer',
    'queue': 'prompt.submit',
    'title': 'session.mutate rename (bare: slash.exec)',
    'undo': 'session.mutate rewind',
    'retry': 'session.mutate rewind + resubmit',
    'tools': 'session.info',
    'model': 'session.mutate model',
    'compress': 'session.mutate compress',
    'new': 'session.mutate reset',
    'clear': 'session.mutate reset',
}
