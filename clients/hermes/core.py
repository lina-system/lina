"""Tool logic for the ``lina-helper`` plugin: Mac names, the per-session computer_use target, helper calls.

Screen reading and input go through Hermes ``computer_use`` (the ``lina-helper-cua`` backend). This plugin
only chooses which Mac that is (``mac_use``) and offers light checks (``mac_status``, ``mac_screenshot``).
"""
import importlib.machinery
import importlib.util
import json
import os
import subprocess
import threading
import uuid

from . import macs

DEFAULT_MAC = 'hub'


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


def fail(mac, code, detail=''):
    return {'ok': False, 'error': {'code': code, 'detail': detail}, 'mac': mac}


def _stderr_line(p):
    lines = (p.stderr or b'').decode('utf-8', 'replace').strip().splitlines()
    return lines[-1][:300] if lines else ''


class Recorder:
    """Wraps the runner so an ssh failure (exit 255, non-JSON) can be told apart from a bad helper reply."""

    def __init__(self, runner):
        self.runner, self.last = runner, None

    def __call__(self, cmd, **kw):
        self.last = self.runner(cmd, **kw)
        return self.last


class Targets:
    """Which Mac each Hermes session's computer_use acts on (process-wide, in memory)."""

    def __init__(self):
        self._lock = threading.Lock()
        self._by_session = {}

    def set(self, session_id, mac):
        with self._lock:
            self._by_session[session_id or ''] = mac

    def get(self, session_id):
        with self._lock:
            return self._by_session.get(session_id or '', DEFAULT_MAC)

    @staticmethod
    def current_session_id():
        """The Hermes session the current tool call belongs to; '' when unknown."""
        try:
            from tools.approval_context import _approval_session_id
            sid = _approval_session_id.get()
            if sid:
                return sid
        except Exception:
            pass
        try:
            from gateway.session_context import get_session_env
            return get_session_env('HERMES_SESSION_ID', '')
        except Exception:
            return os.environ.get('HERMES_SESSION_ID', '')


def _shared_targets():
    """One Targets per process, whichever name this module was imported under.

    Hermes imports the tools plugin under its own module name and the computer-use backend imports it again
    under another; both must see the same per-session targets.
    """
    import sys
    import types
    holder = sys.modules.get('lina_helper_shared')
    if holder is None:
        holder = sys.modules.setdefault('lina_helper_shared', types.ModuleType('lina_helper_shared'))
    if not hasattr(holder, 'targets'):
        holder.targets = Targets()
    return holder.targets


targets = _shared_targets()


def resolve_host(mac, load_config=macs.load):
    """'local' or an ssh alias for a configured Mac name. hub is local even without a config file."""
    config = load_config()
    host = macs.entry(config, mac) if config is not None else None
    if host is None and mac == DEFAULT_MAC:
        return 'local'
    if host is None:
        raise Refused('unknown_mac', 'unknown Mac name; use one of: %s' % (', '.join(macs.names(config)) or DEFAULT_MAC))
    return host


def release_backend(session_id):
    """Drop the session's cached computer_use backend so the next call starts one on the new Mac."""
    try:
        from tools.computer_use.tool import release_computer_use_session
        return release_computer_use_session(session_id)
    except Exception:
        return False


_shared = None


def helper_action(mac, action, args):
    """One helper request on a Mac by name, for the computer-use backend (lease); never raises."""
    global _shared
    if _shared is None:
        _shared = Dispatcher()
    return _shared.helper_action(mac, action, args)


class Dispatcher:
    def __init__(self, load_config=macs.load, runner=subprocess.run, cache_dir=default_cache_dir,
                 targets_=None, release=release_backend, session=Targets.current_session_id):
        self.load_config, self.runner, self.cache_dir = load_config, runner, cache_dir
        self.targets = targets_ or targets
        self.release, self.session = release, session
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
            value = self._call(name, args if isinstance(args, dict) else None)
        except Refused as r:
            value = fail(label, r.code, r.detail)
        except Exception as e:
            value = fail(label, 'internal', type(e).__name__)
        return json.dumps(value, sort_keys=True)

    def _call(self, name, args):
        if args is None:
            raise Refused('bad_request', 'arguments must be an object')
        sid = self.session()
        if name == 'mac_use':
            mac = args.get('mac')
            if not isinstance(mac, str):
                raise Refused('bad_request', 'mac is required')
            resolve_host(mac, self.load_config)
            previous = self.targets.get(sid)
            self.targets.set(sid, mac)
            if previous != mac:
                self.release(sid)
            return {'ok': True, 'mac': mac, 'previous': previous,
                    'note': 'computer_use in this session now acts on %s' % mac}
        mac = args.get('mac') or self.targets.get(sid)
        if name == 'mac_status':
            status = self.helper_action(mac, 'status', {})
            if status.get('ok'):
                cua = self.helper_action(mac, 'cua.status', {})
                if cua.get('ok'):
                    status['result']['cua'] = cua['result']
                status['computer_use_target'] = self.targets.get(sid)
            return status
        if name == 'mac_screenshot':
            display = args.get('display', 'main')
            if display not in (None, 'main') and (not isinstance(display, int) or isinstance(display, bool) or display < 1):
                raise Refused('bad_request', 'display must be "main" or a display id')
            return self.helper_action(mac, 'screenshot', {} if display in (None, 'main') else {'display': display},
                                      screenshot=True)
        raise Refused('bad_request', 'unknown tool')

    def helper_action(self, mac, action, typed, screenshot=False):
        """One lina-helperctl request on a Mac by name; a result dict, never raises."""
        try:
            return self._helper(mac, resolve_host(mac, self.load_config), action, typed, screenshot)
        except Refused as r:
            return fail(mac, r.code, r.detail)

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
