#!/usr/bin/python3
"""computer_use (provider ``lina_helper``) -> LINA Helper's embedded cua-driver. Standard library only.

The provider launches this file as its cua-driver command:
  - `<this> manifest`: the bundled driver's manifest with `mcp_invocation.command` set to this file, so the
    MCP session is launched through us;
  - `<this> mcp [--no-overlay ...]`: exec the bundled driver as a stdio MCP proxy to the helper's audit gate
    socket (`--embedded --socket <gate>`), so every call is checked and audited by LINA Helper and runs with the
    helper's grants. The proxy itself holds no grant.
`call` and `status` go to the same gate (and Mac). Every other verb is passed to the bundled driver unchanged.

LINA_HELPER_CUA_HOST=<ssh alias> (set by the provider from the device list) runs the MCP proxy on that Mac
instead (`ssh <host> cua-driver mcp --embedded --socket <its gate>`), so the proxy is a local child of sshd
there and the gate audits it as the bundled driver. Default: this Mac.

Only cua's `standard` permission mode is supported: `bounded`/`unrestricted` make the upstream backend start
its own private daemon through CuaDriver.app, which would sit outside the helper.
"""
import json
import os
import re
import shlex
import subprocess
import sys

APP = '/Applications/LINA Helper.app'
DRIVER = os.environ.get('LINA_HELPER_CUA_DRIVER', APP + '/Contents/Helpers/cua-driver')  # health: allow HX005 -- own process per MCP session
# LINA_HELPER_CUA_SOCKET is for tests (e.g. a gate forwarded over ssh); operators leave it unset.
GATE = os.environ.get('LINA_HELPER_CUA_SOCKET') or os.path.join(  # health: allow HX005 -- own process per MCP session
    os.path.expanduser('~'), 'Library/Application Support/LINA Helper/run/cua.sock')  # health: allow HX005 -- own process per MCP session
SELF = os.path.realpath(__file__)
HOST = os.environ.get('LINA_HELPER_CUA_HOST', '').strip()  # health: allow HX005 -- own process per MCP session
HOST_RE = r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}'
REMOTE_DRIVER = APP + '/Contents/Helpers/cua-driver'
REMOTE_GATE = '$HOME/Library/Application Support/LINA Helper/run/cua.sock'
SSH = ['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', '-o', 'ServerAliveInterval=15',
       '-o', 'ServerAliveCountMax=3']


def fail(message, code=69):
    sys.stderr.write(f'lina-helper cua: {message}\n')
    return code


def manifest(argv):
    p = subprocess.run([DRIVER, 'manifest', *argv], text=True, encoding='utf-8', errors='replace',
                       stdin=subprocess.DEVNULL, capture_output=True, check=False, timeout=10)
    if p.returncode != 0:
        sys.stderr.write(p.stderr)
        return p.returncode
    try:
        data = json.loads(p.stdout)
    except ValueError:
        return fail('bundled cua-driver returned an unreadable manifest')
    inv = data.get('mcp_invocation') if isinstance(data, dict) else None
    if not isinstance(inv, dict):
        return fail('bundled cua-driver manifest has no mcp_invocation')
    data['mcp_invocation'] = {'command': SELF, 'args': ['mcp']}
    sys.stdout.write(json.dumps(data, indent=2 if '--pretty' in argv or '-p' in argv else None) + '\n')
    return 0


def _strip_endpoint(argv):
    """Drop caller-chosen endpoints (--socket X, --socket=X, --direct, --embedded); we pick the gate."""
    rest, skip = [], False
    for a in argv:
        if skip:
            skip = False
            continue
        if a in ('--socket', '--direct', '--embedded'):
            skip = a == '--socket'
            continue
        if a.startswith('--socket='):
            continue
        rest.append(a)
    return rest


def _run_at_gate(verb, argv, embedded):
    """exec `<driver> <verb> [--embedded] --socket <gate> <argv>` here, or over ssh on LINA_HELPER_CUA_HOST."""
    rest = _strip_endpoint(argv)
    flags = ['--embedded'] if embedded else []
    if HOST:
        if not re.fullmatch(HOST_RE, HOST):
            return fail('LINA_HELPER_CUA_HOST must be a plain ssh alias')
        # $HOME expands on the target; everything else is quoted.
        remote = f'{shlex.join([REMOTE_DRIVER, verb, *flags])} --socket "{REMOTE_GATE}" {shlex.join(rest)}'
        os.execvp('ssh', SSH + [HOST, remote])
    if not os.path.exists(GATE):
        return fail('the helper is not serving cua-driver here (helper not running, profile is not '
                    'operate, or grants are missing); check `lina-helperctl cua.status`')
    os.execv(DRIVER, [DRIVER, verb, *flags, '--socket', GATE, *rest])


def _with_holder(argv):
    """`call <tool> <json>`: add the session's lease holder as `session` when the call has none.

    Every CLI call is a fresh process, so the gate cannot remember the caller's label by process; the
    computer-use backend passes its label in LINA_HELPER_CUA_HOLDER instead.
    """
    holder = os.environ.get('LINA_HELPER_CUA_HOLDER', '').strip()
    if not holder or len(argv) < 2 or argv[1].startswith('-'):
        return argv
    try:
        args = json.loads(argv[1])
    except ValueError:
        return argv
    if not isinstance(args, dict) or args.get('session'):
        return argv
    args['session'] = holder
    return [argv[0], json.dumps(args, separators=(',', ':'))] + argv[2:]


def mcp(argv):
    return _run_at_gate('mcp', argv, embedded=True)


def main(argv):
    if not os.access(DRIVER, os.X_OK):
        return fail(f'bundled cua-driver not found at {DRIVER}')
    if argv[:1] == ['manifest']:
        return manifest(argv[1:])
    if argv[:1] == ['mcp']:
        return mcp(argv[1:])
    if argv[:1] in (['call'], ['status']):
        # Hermes' CLI fallback and readiness probes: same gate (and same Mac) as the MCP proxy.
        return _run_at_gate(argv[0], _with_holder(argv[1:]) if argv[0] == 'call' else argv[1:], embedded=False)
    os.execv(DRIVER, [DRIVER, *argv])


if __name__ == '__main__':
    sys.exit(main(sys.argv[1:]))
