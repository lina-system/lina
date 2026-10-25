"""Tool logic: validate, route by Mac name, call clients/lina-helper, return structured results."""
import importlib.machinery
import importlib.util
import json
import os
import re
import shlex
import subprocess
import uuid

from . import macs

CUA = '/Applications/LINA Helper.app/Contents/Helpers/cua-driver'
CUA_SOCKET = 'Library/Application Support/LINA Helper/run/cua.sock'
CUA_TOOL_RE = r'[a-z_]{1,64}'
CUA_TIMEOUT = 30
CUA_MAX_ARGS = 60 << 10
CUA_MAX_OUTPUT = 8 << 20  # window state can carry an inline image
SSH_OPTS = ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', '-o', 'ServerAliveInterval=5',
            '-o', 'ServerAliveCountMax=2']
STR_MAX = 1024


class Refused(Exception):
    def __init__(self, code, detail=''):
        super().__init__(code)
        self.code, self.detail = code, detail


def load_client():
    """clients/lina-helper as a module. The plugin dir must be a symlink into clients/ (see README)."""
    path = os.environ.get('LINA_HELPER_CLIENT') or os.path.join(
        os.path.dirname(os.path.dirname(os.path.realpath(__file__))), 'lina-helper')
    loader = importlib.machinery.SourceFileLoader('lina_helper_client', path)
    spec = importlib.util.spec_from_loader('lina_helper_client', loader)
    module = importlib.util.module_from_spec(spec)
    try:
        loader.exec_module(module)
    except OSError:
        raise Refused('client_missing', 'clients/lina-helper not found next to the plugin')
    return module


def default_cache_dir():
    """LINA_HELPER_CACHE_DIR, else <hermes home>/cache/lina-helper (profile-relative), else ~/.cache/lina-helper."""
    env = os.environ.get('LINA_HELPER_CACHE_DIR')
    if env:
        return env
    try:
        from hermes_constants import get_hermes_home
        return os.path.join(str(get_hermes_home()), 'cache', 'lina-helper')
    except Exception:
        return os.path.join(os.path.expanduser('~'), '.cache', 'lina-helper')


def _is_number(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def _is_int(v):
    return isinstance(v, int) and not isinstance(v, bool)


def _string(args, key, default=None, required=False):
    v = args.get(key, default)
    if v is None:
        if required:
            raise Refused('bad_request', '%s is required' % key)
        return None
    if not isinstance(v, str) or not v or len(v) > STR_MAX:
        raise Refused('bad_request', '%s must be a non-empty string' % key)
    return v


def _int(args, key, lo, hi):
    v = args.get(key)
    if v is None:
        return None
    if not _is_int(v) or not lo <= v <= hi:
        raise Refused('bad_request', '%s must be an integer in %d..%d' % (key, lo, hi))
    return v


def _build_status(args):
    return 'status', {}


def _build_screenshot(args):
    display = args.get('display', 'main')
    if display == 'main' or display is None:
        return 'screenshot', {}
    if not _is_int(display) or display < 1:
        raise Refused('bad_request', 'display must be "main" or a display id')
    return 'screenshot', {'display': display}


def _build_ax_tree(args):
    out = {'app': _string(args, 'app', 'frontmost')}
    for key, hi in (('depth', 8), ('max_nodes', 2000)):
        v = _int(args, key, 1, hi)
        if v is not None:
            out[key] = v
    return 'ax.tree', out


def _build_ax_press(args):
    out = {'app': _string(args, 'app', required=True), 'path': _string(args, 'path', required=True)}
    role = _string(args, 'expect_role')
    if role:
        out['expect_role'] = role
    return 'ax.press', out


def _build_click(args):
    out = {}
    for key in ('x', 'y'):
        if not _is_number(args.get(key)):
            raise Refused('bad_request', '%s must be a number' % key)
        out[key] = args[key]
    button = _string(args, 'button')
    if button:
        out['button'] = button
    return 'input.click', out


def _build_keys(args):
    text, key, mods = args.get('text'), args.get('key'), args.get('modifiers')
    if (text is None) == (key is None):
        raise Refused('bad_request', 'give exactly one of text or key')
    if text is not None:
        if not isinstance(text, str) or not text or len(text) > 512 or mods is not None:
            raise Refused('bad_request', 'text must be 1-512 characters and takes no modifiers')
        return 'input.keys', {'text': text}
    out = {'key': _string(args, 'key')}
    if mods is not None:
        if not isinstance(mods, list) or len(mods) > 8 or not all(isinstance(m, str) and 0 < len(m) <= 32 for m in mods):
            raise Refused('bad_request', 'modifiers must be a list of names')
        out['modifiers'] = mods
    return 'input.keys', out


BUILDERS = {
    'mac_status': _build_status, 'mac_screenshot': _build_screenshot, 'mac_ax_tree': _build_ax_tree,
    'mac_ax_press': _build_ax_press, 'mac_click': _build_click, 'mac_keys': _build_keys,
}
TOOLS = tuple(BUILDERS) + ('mac_cua',)


def fail(mac, code, detail=''):
    return {'ok': False, 'error': {'code': code, 'detail': detail}, 'mac': mac}


class Recorder:
    """Wraps the runner so an ssh failure (exit 255, non-JSON) can be told apart from a bad helper reply."""

    def __init__(self, runner):
        self.runner, self.last = runner, None

    def __call__(self, cmd, **kw):
        self.last = self.runner(cmd, **kw)
        return self.last


def _stderr_line(p):
    lines = (p.stderr or b'').decode('utf-8', 'replace').strip().splitlines()
    return lines[-1][:300] if lines else ''


class Dispatcher:
    def __init__(self, load_config=macs.load, runner=subprocess.run, cache_dir=default_cache_dir):
        self.load_config, self.runner, self.cache_dir = load_config, runner, cache_dir
        self._client = None

    def client(self):
        if self._client is None:
            self._client = load_client()
        return self._client

    def call(self, name, args):
        """JSON string for Hermes; never raises."""
        mac = args.get('mac') if isinstance(args, dict) else None
        label = mac if isinstance(mac, str) and len(mac) <= 64 else None
        try:
            value = self._call(name, args)
        except Refused as r:
            value = fail(label, r.code, r.detail)
        except Exception as e:
            value = fail(label, 'internal', type(e).__name__)
        return json.dumps(value, sort_keys=True)

    def _target(self, args):
        if not isinstance(args, dict):
            raise Refused('bad_request', 'arguments must be an object')
        mac = args.get('mac')
        config = self.load_config()
        if config is None:
            raise Refused('not_configured', 'no Macs are configured')
        host = macs.entry(config, mac)
        if host is None:
            raise Refused('unknown_mac', 'unknown Mac name; use one of: %s' % ', '.join(macs.names(config)))
        return host

    def _call(self, name, args):
        if name not in TOOLS:
            raise Refused('bad_request', 'unknown tool')
        host = self._target(args)
        mac = args['mac']
        if name == 'mac_cua':
            return self._cua(mac, host, args)
        action, typed = BUILDERS[name](args)
        return self._helper(mac, host, action, typed, name == 'mac_screenshot')

    def _helper(self, mac, host, action, typed, screenshot):
        lh = self.client()
        local = host == 'local'
        out = None
        if screenshot:
            directory = self.cache_dir()
            try:
                os.makedirs(directory, mode=0o700, exist_ok=True)
            except OSError as e:
                raise Refused('output_unwritable', e.strerror or 'cache directory not writable')
            out = os.path.join(directory, 'mac-%s-%s.png' % (mac, uuid.uuid4().hex))
        rec = Recorder(self.runner)
        try:
            code, value = lh.invoke(host, action, out=out, runner=rec, json_args=json.dumps(typed) if typed else None,
                                    local=local, connect_timeout=10)
        except lh.UsageError as e:
            raise Refused('bad_request', str(e))
        except subprocess.TimeoutExpired:
            raise Refused('timeout', 'no answer in time')
        except OSError as e:
            raise Refused('ssh_failed' if not local else 'exec_failed', e.strerror or str(e))
        if value.get('ok') is not True or code != 0:
            error = value.get('error') or {}
            detail = error.get('detail') or ''
            err_code = error.get('code') or 'internal'
            p = rec.last
            if err_code == 'transport_timeout':
                err_code = 'timeout'
            if err_code == 'invalid_remote_response' and p is not None and not local and p.returncode == 255:
                err_code, detail = 'ssh_failed', _stderr_line(p)
            return fail(mac, err_code, detail if isinstance(detail, str) else json.dumps(detail))
        result = value.get('result') or {}
        if not screenshot:
            return {'ok': True, 'mac': mac, 'result': result}
        return {'ok': True, 'mac': mac, 'path': out, 'bytes': os.path.getsize(out),
                'width': result.get('width'), 'height': result.get('height')}

    def _cua(self, mac, host, args):
        tool = args.get('tool')
        if not isinstance(tool, str) or not re.fullmatch(CUA_TOOL_RE, tool):
            raise Refused('bad_request', 'tool must match ^[a-z_]{1,64}$')
        tool_args = args.get('args', {})
        if not isinstance(tool_args, dict):
            raise Refused('bad_request', 'args must be an object')
        payload = json.dumps(tool_args, separators=(',', ':'), sort_keys=True)
        if len(payload) > CUA_MAX_ARGS:
            raise Refused('bad_request', 'args too large')
        lh = self.client()
        if host == 'local':
            cmd = [CUA, 'call', '--socket', os.path.join(os.path.expanduser('~'), CUA_SOCKET), tool, payload]
        else:
            if not re.fullmatch(lh.HOST_RE, host):
                raise Refused('bad_request', 'configured host is not a valid SSH alias')
            # $HOME must expand on the target, so that part is double-quoted by hand; the rest is shlex-quoted.
            remote = '%s "$HOME/%s" %s' % (shlex.join([CUA, 'call', '--socket']), CUA_SOCKET, shlex.join([tool, payload]))
            cmd = ['ssh'] + SSH_OPTS + [host, remote]
        try:
            p = self.runner(cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=CUA_TIMEOUT, check=False)
        except subprocess.TimeoutExpired:
            raise Refused('timeout', 'no answer in time')
        except OSError as e:
            raise Refused('ssh_failed' if host != 'local' else 'exec_failed', e.strerror or str(e))
        if p.returncode != 0:
            if host != 'local' and p.returncode == 255:
                raise Refused('ssh_failed', _stderr_line(p))
            raise Refused('cua_failed', _stderr_line(p) or 'exit %d' % p.returncode)
        if len(p.stdout) > CUA_MAX_OUTPUT:
            raise Refused('response_too_large', 'cua-driver returned %d bytes; limit is %d' % (len(p.stdout), CUA_MAX_OUTPUT))
        text = p.stdout.decode('utf-8', 'replace').strip()
        try:
            result = json.loads(text)
        except ValueError:
            result = text
        return {'ok': True, 'mac': mac, 'result': result}
