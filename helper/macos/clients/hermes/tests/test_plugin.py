import importlib.util
import json
import os
import shlex
import subprocess
import sys
import tempfile
import unittest
import unittest.mock

HERE = os.path.dirname(os.path.abspath(__file__))
PLUGIN = os.path.normpath(os.path.join(HERE, '..'))

spec = importlib.util.spec_from_file_location(
    'lina_helper_plugin', os.path.join(PLUGIN, '__init__.py'), submodule_search_locations=[PLUGIN])
plugin = importlib.util.module_from_spec(spec)
sys.modules['lina_helper_plugin'] = plugin
spec.loader.exec_module(plugin)
from lina_helper_plugin import core, macs  # noqa: E402

CTL = '/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl'
CONFIG = {'macs': {'agent-hub': {'host': 'local'}, 'agent-mac': {'host': 'lina-box'}}}


def ok(result=None):
    return (json.dumps({'v': 1, 'ok': True, 'result': result or {}}) + '\n').encode()


def err(code, detail=None):
    e = {'code': code}
    if detail:
        e['detail'] = detail
    return (json.dumps({'v': 1, 'ok': False, 'error': e}) + '\n').encode()


class Result:
    def __init__(self, stdout=b'', stderr=b'', returncode=0):
        self.stdout, self.stderr, self.returncode = stdout, stderr, returncode


class Fake:
    def __init__(self, result=None, exc=None):
        self.result, self.exc, self.calls = result or Result(ok()), exc, []

    def __call__(self, cmd, **kw):
        self.calls.append((cmd, kw))
        if self.exc:
            raise self.exc
        return self.result


def remote_of(cmd):
    """The helperctl argv a call would run, whether local or via ssh."""
    return shlex.split(cmd[-1]) if cmd[0] == 'ssh' else cmd


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.cache = os.path.join(self.tmp.name, 'cache')
        self.targets, self.released = core.Targets(), []

    def tool(self, name, args, fake=None, config=CONFIG):
        fake = fake or Fake()
        d = core.Dispatcher(lambda: config, runner=fake, cache_dir=lambda: self.cache, targets_=self.targets,
                            release=self.released.append, session=lambda: 'sess-1')
        return json.loads(d.call(name, args)), fake


class RoutingTests(Base):
    def test_local_runs_ctl_directly(self):
        out, fake = self.tool('mac_status', {'mac': 'agent-hub'})
        self.assertEqual([c[0] for c in fake.calls], [[CTL, 'status'], [CTL, 'cua.status']])
        self.assertTrue(out['ok'])
        self.assertEqual(out['mac'], 'agent-hub')
        self.assertEqual(out['computer_use_target'], 'agent-hub')

    def test_ssh_argv(self):
        out, fake = self.tool('mac_status', {'mac': 'agent-mac'})
        self.assertEqual(fake.calls[0][0], [
            'ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', '-o', 'ServerAliveInterval=5',
            '-o', 'ServerAliveCountMax=2', 'lina-box', shlex.join([CTL, 'status'])])

    def test_unknown_mac_refused_before_subprocess(self):
        for mac in ['nope', 5, 'lina-box', '-oProxyCommand=x', 'agent-hub ']:
            out, fake = self.tool('mac_status', {'mac': mac})
            self.assertEqual(fake.calls, [], mac)
            self.assertFalse(out['ok'])
            self.assertEqual(out['error']['code'], 'unknown_mac')

    def test_missing_mac_argument_uses_the_session_target(self):
        out, fake = self.tool('mac_status', {})
        self.assertEqual(fake.calls[0][0], [CTL, 'status'])
        self.targets.set('sess-1', 'agent-mac')
        out, fake = self.tool('mac_status', {})
        self.assertEqual(fake.calls[0][0][-2], 'lina-box')

    def test_missing_config_still_knows_hub_mac(self):
        out, fake = self.tool('mac_status', {'mac': 'agent-hub'}, config=None)
        self.assertEqual(fake.calls[0][0], [CTL, 'status'])
        out, fake = self.tool('mac_status', {'mac': 'agent-mac'}, config=None)
        self.assertEqual(fake.calls, [])
        self.assertEqual(out['error']['code'], 'unknown_mac')

    def test_bad_host_in_config_is_refused(self):
        cfg = {'macs': {'x': {'host': '-oProxyCommand=evil'}}}
        out, fake = self.tool('mac_status', {'mac': 'x'}, config=cfg)
        self.assertEqual(fake.calls, [])
        self.assertFalse(out['ok'])

    def test_never_raises(self):
        d = core.Dispatcher(lambda: 1 / 0, runner=Fake(), cache_dir=lambda: self.cache, session=lambda: 's')
        self.assertFalse(json.loads(d.call('mac_status', {'mac': 'a'}))['ok'])
        self.assertFalse(json.loads(d.call('mac_status', 'junk'))['ok'])


class UseTests(Base):
    def test_switch_target_and_release_backend(self):
        out, fake = self.tool('mac_use', {'mac': 'agent-mac'})
        self.assertEqual(out, {'ok': True, 'mac': 'agent-mac', 'previous': 'agent-hub',
                               'note': 'computer_use in this session now acts on agent-mac'})
        self.assertEqual(fake.calls, [])
        self.assertEqual(self.targets.get('sess-1'), 'agent-mac')
        self.assertEqual(self.targets.get('other'), 'agent-hub')
        self.assertEqual(self.released, ['sess-1'])

    def test_same_target_does_not_restart(self):
        self.tool('mac_use', {'mac': 'agent-hub'})
        self.assertEqual(self.released, [])

    def test_unknown_mac_refused_and_target_kept(self):
        for mac in ['nope', None, 5, 'lina-box']:
            out, fake = self.tool('mac_use', {'mac': mac})
            self.assertFalse(out['ok'], mac)
            self.assertEqual(fake.calls, [])
        self.assertEqual(self.targets.get('sess-1'), 'agent-hub')
        self.assertEqual(self.released, [])

    def test_status_reports_cua_and_lease(self):
        calls = []

        def runner(cmd, **kw):
            calls.append(cmd)
            if cmd[-1] == 'cua.status':
                return Result(ok({'running': True, 'lease': {'holder': 'hermes-x', 'expires_in_s': 200}}))
            return Result(ok({'policy': {'profile': 'operate'}}))
        out, _ = self.tool('mac_status', {'mac': 'agent-hub'}, runner)
        self.assertEqual(out['result']['cua']['lease']['holder'], 'hermes-x')

    def test_screenshot_bad_display_is_bad_request(self):
        for display in ['second', 0, True]:
            out, fake = self.tool('mac_screenshot', {'mac': 'agent-hub', 'display': display})
            self.assertEqual(fake.calls, [])
            self.assertEqual(out['error']['code'], 'bad_request')

    def test_targets_are_shared_across_imports(self):
        spec2 = importlib.util.spec_from_file_location(
            'lina_helper_plugin_again', os.path.join(PLUGIN, '__init__.py'), submodule_search_locations=[PLUGIN])
        again = importlib.util.module_from_spec(spec2)
        sys.modules['lina_helper_plugin_again'] = again
        spec2.loader.exec_module(again)
        self.assertIs(sys.modules['lina_helper_plugin_again.core'].targets, core.targets)

    def test_module_level_helper_action_for_the_backend(self):
        self.assertTrue(callable(core.helper_action))
        with unittest.mock.patch.object(core, '_shared', core.Dispatcher(lambda: CONFIG, runner=Fake(), session=lambda: 's')):
            self.assertTrue(core.helper_action('agent-hub', 'cua.release', {'holder': 'h'})['ok'])

    def test_helper_action_never_raises(self):
        d = core.Dispatcher(lambda: CONFIG, runner=Fake(Result(ok({'holder': 'h'}))), session=lambda: 's')
        self.assertEqual(d.helper_action('agent-hub', 'cua.lease', {'holder': 'h'})['result'], {'holder': 'h'})
        self.assertEqual(d.helper_action('nope', 'cua.lease', {})['error']['code'], 'unknown_mac')


class ScreenshotTests(Base):
    PNG = b'\x89PNG\r\n\x1a\nbytes'

    def shot(self, args=None, result=None):
        fake = Fake(result or Result(self.PNG, ok({'width': 800, 'height': 600, 'path': '/remote/x.png'})))
        out, fake = self.tool('mac_screenshot', dict(args or {}, mac='agent-mac'), fake)
        return out, fake

    def test_writes_png_into_cache_dir(self):
        out, fake = self.shot()
        self.assertTrue(out['ok'], out)
        self.assertEqual(os.path.dirname(out['path']), self.cache)
        self.assertTrue(out['path'].endswith('.png'))
        with open(out['path'], 'rb') as f:
            self.assertEqual(f.read(), self.PNG)
        self.assertEqual((out['width'], out['height'], out['bytes']), (800, 600, len(self.PNG)))
        self.assertNotIn('/remote/', json.dumps(out))
        self.assertEqual(oct(os.stat(self.cache).st_mode & 0o777), '0o700')
        argv = remote_of(fake.calls[0][0])
        self.assertEqual(argv, [CTL, 'screenshot', '--out', '-'])

    def test_display_passed_typed(self):
        out, fake = self.shot({'display': 2})
        argv = remote_of(fake.calls[0][0])
        self.assertEqual(json.loads(argv[argv.index('--json') + 1]), {'display': 2})
        out, fake = self.shot({'display': 'main'})
        self.assertNotIn('--json', remote_of(fake.calls[0][0]))

    def test_unique_paths(self):
        a, _ = self.shot()
        b, _ = self.shot()
        self.assertNotEqual(a['path'], b['path'])

    def test_helper_error_leaves_no_file(self):
        out, _ = self.shot(result=Result(b'', err('permission_missing', 'screen_recording'), 1))
        self.assertFalse(out['ok'])
        self.assertEqual(out['error'], {'code': 'permission_missing', 'detail': 'screen_recording'})
        self.assertEqual(os.listdir(self.cache) if os.path.isdir(self.cache) else [], [])

    def test_unwritable_cache_dir(self):
        blocker = os.path.join(self.tmp.name, 'file')
        open(blocker, 'w').close()
        self.cache = os.path.join(blocker, 'sub')
        out, fake = self.shot()
        self.assertEqual(out['error']['code'], 'output_unwritable')
        self.assertEqual(fake.calls, [])


class ErrorTests(Base):
    def test_helper_codes_map_to_structured_results(self):
        for code in ['not_allowed', 'permission_missing', 'bad_request', 'timeout', 'busy', 'target_not_found']:
            out, _ = self.tool('mac_status', {'mac': 'agent-hub'}, Fake(Result(err(code, 'd'), b'', 1)))
            self.assertEqual(out, {'ok': False, 'error': {'code': code, 'detail': 'd'}, 'mac': 'agent-hub'})

    def test_error_without_detail_has_empty_detail(self):
        out, _ = self.tool('mac_status', {'mac': 'agent-hub'}, Fake(Result(err('busy'), b'', 1)))
        self.assertEqual(out['error'], {'code': 'busy', 'detail': ''})

    def test_ssh_failure(self):
        fake = Fake(Result(b'', b'ssh: Could not resolve hostname lina-box\n', 255))
        out, _ = self.tool('mac_status', {'mac': 'agent-mac'}, fake)
        self.assertFalse(out['ok'])
        self.assertEqual(out['error']['code'], 'ssh_failed')
        self.assertIn('Could not resolve', out['error']['detail'])
        self.assertEqual(out['mac'], 'agent-mac')

    def test_ssh_binary_missing(self):
        out, _ = self.tool('mac_status', {'mac': 'agent-mac'}, Fake(exc=FileNotFoundError('ssh')))
        self.assertEqual(out['error']['code'], 'ssh_failed')

    def test_timeout(self):
        out, _ = self.tool('mac_status', {'mac': 'agent-mac'}, Fake(exc=subprocess.TimeoutExpired('ssh', 30)))
        self.assertFalse(out['ok'])
        self.assertEqual(out['error']['code'], 'timeout')

    def test_garbage_output(self):
        out, _ = self.tool('mac_status', {'mac': 'agent-hub'}, Fake(Result(b'hello\n', b'', 0)))
        self.assertEqual(out['error']['code'], 'invalid_remote_response')

    def test_success_carries_result(self):
        out, _ = self.tool('mac_screenshot', {'mac': 'agent-hub'}, Fake(Result(b'PNG', ok({'width': 7, 'height': 8}))))
        self.assertEqual((out['ok'], out['mac'], out['width'], out['height']), (True, 'agent-hub', 7, 8))


class ConfigTests(unittest.TestCase):
    def write(self, text):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        p = os.path.join(d.name, 'macs.yaml')
        with open(p, 'w') as f:
            f.write(text)
        return p

    def test_json_in_macs_yaml(self):
        p = self.write(json.dumps(CONFIG))
        self.assertEqual(macs.load(p), CONFIG)

    def test_env_override_path(self):
        p = self.write(json.dumps(CONFIG))
        self.assertEqual(macs.config_path({'LINA_HELPER_MACS_FILE': p}), p)
        self.assertTrue(macs.config_path({}).endswith(os.path.join('hermes', 'macs.yaml')))

    def test_missing_or_broken_is_none(self):
        self.assertIsNone(macs.load('/nonexistent/macs.yaml'))
        self.assertIsNone(macs.load(self.write('{not json')))
        self.assertIsNone(macs.load(self.write('[1,2]')))
        self.assertIsNone(macs.load(self.write('{"macs": []}')))

    def test_names_skip_invalid_entries(self):
        cfg = {'macs': {'ok': {'host': 'h'}, 'bad name': {'host': 'h'}, 'nohost': {}, 'str': 'h', '-x': {'host': 'h'}}}
        self.assertEqual(macs.names(cfg), ['ok'])
        self.assertEqual(macs.names(None), [])

    def test_example_file_parses_when_a_yaml_library_exists(self):
        try:
            import yaml  # noqa: F401
        except ImportError:
            try:
                import ruamel.yaml  # noqa: F401
            except ImportError:
                self.skipTest('no YAML library in this interpreter')
        cfg = macs.load(os.path.join(PLUGIN, 'macs.example.yaml'))
        self.assertEqual(macs.names(cfg), ['agent-hub', 'agent-mac'])


class RegisterTests(Base):
    class Ctx:
        def __init__(self):
            self.tools = {}

        def register_tool(self, **kw):
            self.tools[kw['name']] = kw

    def register(self, env):
        ctx = self.Ctx()
        old = dict(os.environ)
        os.environ.update(env)
        try:
            plugin.register(ctx)
        finally:
            os.environ.clear()
            os.environ.update(old)
        return ctx.tools

    def test_three_tools(self):
        tools = self.register({'LINA_HELPER_MACS_FILE': '/nonexistent/macs.yaml'})
        self.assertEqual(sorted(tools), ['mac_screenshot', 'mac_status', 'mac_use'])
        for name, t in tools.items():
            s = t['schema']
            self.assertEqual(s['name'], name)
            self.assertEqual(s['parameters']['type'], 'object')
            self.assertEqual(s['parameters']['properties']['mac']['type'], 'string')
            self.assertEqual(s['parameters']['properties']['mac']['enum'], ['agent-hub'])
            self.assertEqual(t['toolset'], 'lina_helper')
            self.assertTrue(callable(t['handler']))
        self.assertEqual(tools['mac_use']['schema']['parameters']['required'], ['mac'])
        self.assertEqual(tools['mac_status']['schema']['parameters']['required'], [])

    def test_enum_lists_names_only(self):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        p = os.path.join(d.name, 'macs.yaml')
        with open(p, 'w') as f:
            json.dump({'macs': {'agent-hub': {'host': 'local'}, 'agent-mac': {'host': 'secret-host.example'}}}, f)
        tools = self.register({'LINA_HELPER_MACS_FILE': p})
        for t in tools.values():
            self.assertEqual(t['schema']['parameters']['properties']['mac']['enum'], ['agent-hub', 'agent-mac'])
            self.assertNotIn('secret-host', json.dumps(t['schema']))

    def test_handler_accepts_dispatch_kwargs_and_returns_json_string(self):
        tools = self.register({'LINA_HELPER_MACS_FILE': '/nonexistent/macs.yaml'})
        out = tools['mac_use']['handler']({'mac': 'x'}, task_id='t')
        self.assertFalse(json.loads(out)['ok'])

    def test_manifest_lists_the_tools(self):
        with open(os.path.join(PLUGIN, 'plugin.yaml')) as f:
            text = f.read()
        self.assertIn('name: lina-helper', text)
        self.assertIn('kind: standalone', text)
        for name in ['mac_use', 'mac_status', 'mac_screenshot']:
            self.assertIn('  - ' + name, text)


if __name__ == '__main__':
    unittest.main()
