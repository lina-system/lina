import importlib.util
import json
import os
import shlex
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
PLUGIN = os.path.normpath(os.path.join(HERE, '..'))

spec = importlib.util.spec_from_file_location(
    'lina_helper_plugin', os.path.join(PLUGIN, '__init__.py'), submodule_search_locations=[PLUGIN])
plugin = importlib.util.module_from_spec(spec)
sys.modules['lina_helper_plugin'] = plugin
spec.loader.exec_module(plugin)
from lina_helper_plugin import core, macs  # noqa: E402

CTL = '/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl'
CUA = '/Applications/LINA Helper.app/Contents/Helpers/cua-driver'
CONFIG = {'macs': {'hub': {'host': 'local'}, 'agent-mac': {'host': 'lina-box'}}}


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

    def tool(self, name, args, fake=None, config=CONFIG):
        fake = fake or Fake()
        d = core.Dispatcher(lambda: config, runner=fake, cache_dir=lambda: self.cache)
        return json.loads(d.call(name, args)), fake


class RoutingTests(Base):
    def test_local_runs_ctl_directly(self):
        out, fake = self.tool('mac_status', {'mac': 'hub'})
        self.assertEqual(fake.calls[0][0], [CTL, 'status'])
        self.assertTrue(out['ok'])
        self.assertEqual(out['mac'], 'hub')

    def test_ssh_argv(self):
        out, fake = self.tool('mac_status', {'mac': 'agent-mac'})
        self.assertEqual(fake.calls[0][0], [
            'ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', '-o', 'ServerAliveInterval=5',
            '-o', 'ServerAliveCountMax=2', 'lina-box', shlex.join([CTL, 'status'])])

    def test_unknown_mac_refused_before_subprocess(self):
        for mac in ['nope', '', None, 5, 'lina-box', '-oProxyCommand=x', 'hub ']:
            out, fake = self.tool('mac_status', {'mac': mac})
            self.assertEqual(fake.calls, [], mac)
            self.assertFalse(out['ok'])
            self.assertEqual(out['error']['code'], 'unknown_mac')

    def test_missing_mac_argument(self):
        out, fake = self.tool('mac_status', {})
        self.assertEqual(fake.calls, [])
        self.assertEqual(out['error']['code'], 'unknown_mac')

    def test_missing_config(self):
        out, fake = self.tool('mac_status', {'mac': 'hub'}, config=None)
        self.assertEqual(fake.calls, [])
        self.assertEqual(out['error']['code'], 'not_configured')
        self.assertEqual(out['mac'], 'hub')

    def test_bad_host_in_config_is_refused(self):
        cfg = {'macs': {'x': {'host': '-oProxyCommand=evil'}}}
        out, fake = self.tool('mac_status', {'mac': 'x'}, config=cfg)
        self.assertEqual(fake.calls, [])
        self.assertFalse(out['ok'])

    def test_never_raises(self):
        d = core.Dispatcher(lambda: 1 / 0, runner=Fake(), cache_dir=lambda: self.cache)
        self.assertFalse(json.loads(d.call('mac_status', {'mac': 'a'}))['ok'])
        self.assertFalse(json.loads(d.call('mac_status', 'junk'))['ok'])


class MappingTests(Base):
    def run_tool(self, name, args, mac='hub'):
        out, fake = self.tool(name, dict(args, mac=mac))
        self.assertTrue(out['ok'], out)
        return remote_of(fake.calls[0][0])

    def json_arg(self, argv):
        return json.loads(argv[argv.index('--json') + 1])

    def test_ax_tree(self):
        argv = self.run_tool('mac_ax_tree', {'app': 'com.apple.finder', 'depth': 3, 'max_nodes': 100})
        self.assertEqual(argv[:2], [CTL, 'ax.tree'])
        self.assertEqual(self.json_arg(argv), {'app': 'com.apple.finder', 'depth': 3, 'max_nodes': 100})

    def test_ax_tree_defaults_to_frontmost(self):
        argv = self.run_tool('mac_ax_tree', {})
        self.assertEqual(self.json_arg(argv), {'app': 'frontmost'})

    def test_ax_press(self):
        argv = self.run_tool('mac_ax_press', {'app': 'frontmost', 'path': '0/2/1', 'expect_role': 'AXButton'})
        self.assertEqual(argv[:2], [CTL, 'ax.press'])
        self.assertEqual(self.json_arg(argv), {'app': 'frontmost', 'path': '0/2/1', 'expect_role': 'AXButton'})

    def test_ax_press_without_expect_role(self):
        argv = self.run_tool('mac_ax_press', {'app': 'frontmost', 'path': 'p'})
        self.assertEqual(self.json_arg(argv), {'app': 'frontmost', 'path': 'p'})

    def test_click(self):
        argv = self.run_tool('mac_click', {'x': 10, 'y': 20.5, 'button': 'right'})
        self.assertEqual(argv[:2], [CTL, 'input.click'])
        self.assertEqual(self.json_arg(argv), {'x': 10, 'y': 20.5, 'button': 'right'})

    def test_click_default_button(self):
        self.assertEqual(self.json_arg(self.run_tool('mac_click', {'x': 1, 'y': 2})), {'x': 1, 'y': 2})

    def test_keys_text(self):
        argv = self.run_tool('mac_keys', {'text': "it's $(x)"})
        self.assertEqual(argv[:2], [CTL, 'input.keys'])
        self.assertEqual(self.json_arg(argv), {'text': "it's $(x)"})

    def test_keys_key_with_modifiers(self):
        argv = self.run_tool('mac_keys', {'key': 'c', 'modifiers': ['command']}, mac='agent-mac')
        self.assertEqual(self.json_arg(argv), {'key': 'c', 'modifiers': ['command']})

    def test_bad_arguments_are_bad_request_without_subprocess(self):
        cases = [
            ('mac_ax_tree', {'depth': 'deep'}), ('mac_ax_tree', {'depth': True}), ('mac_ax_tree', {'app': 5}),
            ('mac_ax_press', {'app': 'frontmost'}), ('mac_ax_press', {'path': 'p'}),
            ('mac_click', {'x': 1}), ('mac_click', {'x': 'a', 'y': 2}), ('mac_click', {'x': True, 'y': 1}),
            ('mac_keys', {}), ('mac_keys', {'text': 'a', 'key': 'b'}), ('mac_keys', {'key': 'a', 'modifiers': 'cmd'}),
            ('mac_keys', {'text': 'a', 'modifiers': ['cmd']}), ('mac_screenshot', {'display': 'second'}),
            ('mac_screenshot', {'display': 0}), ('mac_screenshot', {'display': True}),
        ]
        for name, args in cases:
            out, fake = self.tool(name, dict(args, mac='hub'))
            self.assertEqual(fake.calls, [], (name, args))
            self.assertEqual(out['error']['code'], 'bad_request', (name, args))
            self.assertEqual(out['mac'], 'hub')


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
            out, _ = self.tool('mac_status', {'mac': 'hub'}, Fake(Result(err(code, 'd'), b'', 1)))
            self.assertEqual(out, {'ok': False, 'error': {'code': code, 'detail': 'd'}, 'mac': 'hub'})

    def test_error_without_detail_has_empty_detail(self):
        out, _ = self.tool('mac_status', {'mac': 'hub'}, Fake(Result(err('busy'), b'', 1)))
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
        out, _ = self.tool('mac_status', {'mac': 'hub'}, Fake(Result(b'hello\n', b'', 0)))
        self.assertEqual(out['error']['code'], 'invalid_remote_response')

    def test_success_carries_result(self):
        out, _ = self.tool('mac_status', {'mac': 'hub'}, Fake(Result(ok({'pid': 7}))))
        self.assertEqual(out, {'ok': True, 'mac': 'hub', 'result': {'pid': 7}})


class CuaTests(Base):
    def test_local_argv(self):
        out, fake = self.tool('mac_cua', {'mac': 'hub', 'tool': 'get_window_state', 'args': {'pid': 3, 'a': 'b'}},
                              Fake(Result(b'{"windows":[]}\n')))
        sock = os.path.join(os.path.expanduser('~'), 'Library/Application Support/LINA Helper/run/cua.sock')
        self.assertEqual(fake.calls[0][0], [CUA, 'call', '--socket', sock, 'get_window_state', '{"a":"b","pid":3}'])
        self.assertEqual(out, {'ok': True, 'mac': 'hub', 'result': {'windows': []}})

    def test_ssh_argv_expands_home_on_the_remote(self):
        _, fake = self.tool('mac_cua', {'mac': 'agent-mac', 'tool': 'list_apps', 'args': {}}, Fake(Result(b'{}')))
        cmd = fake.calls[0][0]
        self.assertEqual(cmd[:2], ['ssh', '-T'])
        self.assertEqual(cmd[-2], 'lina-box')
        self.assertEqual(
            cmd[-1],
            "'/Applications/LINA Helper.app/Contents/Helpers/cua-driver' call --socket "
            '"$HOME/Library/Application Support/LINA Helper/run/cua.sock" list_apps \'{}\'')

    def test_args_default_empty(self):
        _, fake = self.tool('mac_cua', {'mac': 'hub', 'tool': 'list_apps'}, Fake(Result(b'{}')))
        self.assertEqual(fake.calls[0][0][-1], '{}')

    def test_hostile_json_is_quoted(self):
        _, fake = self.tool('mac_cua', {'mac': 'agent-mac', 'tool': 'type_text', 'args': {'text': "'; rm -rf / #"}},
                            Fake(Result(b'{}')))
        parts = shlex.split(fake.calls[0][0][-1].replace('"$HOME', '"/h'))
        self.assertEqual(json.loads(parts[-1]), {'text': "'; rm -rf / #"})
        self.assertEqual(parts[-2], 'type_text')

    def test_tool_name_validation(self):
        for bad in ['', 'a-b', 'A', 'x y', '../x', 'a' * 65, 'a;b', 'tool\n', '-x', None, 3, 'é']:
            out, fake = self.tool('mac_cua', {'mac': 'hub', 'tool': bad, 'args': {}})
            self.assertEqual(fake.calls, [], bad)
            self.assertEqual(out['error']['code'], 'bad_request', bad)
        for good in ['a', 'get_window_state', 'a' * 64]:
            out, _ = self.tool('mac_cua', {'mac': 'hub', 'tool': good, 'args': {}}, Fake(Result(b'{}')))
            self.assertTrue(out['ok'], good)

    def test_args_must_be_object(self):
        for bad in [[], 'x', 3, None.__class__]:
            out, fake = self.tool('mac_cua', {'mac': 'hub', 'tool': 'x', 'args': bad})
            self.assertEqual(fake.calls, [])
            self.assertEqual(out['error']['code'], 'bad_request')
        out, fake = self.tool('mac_cua', {'mac': 'hub', 'tool': 'x', 'args': {'a': 'x' * 70000}})
        self.assertEqual(out['error']['code'], 'bad_request')

    def test_oversized_response_is_an_error_not_truncated(self):
        big = json.dumps({'blob': 'x' * (8 << 20)}).encode()
        out, _ = self.tool('mac_cua', {'mac': 'hub', 'tool': 'get_window_state'}, Fake(Result(big)))
        self.assertFalse(out['ok'])
        self.assertEqual(out['error']['code'], 'response_too_large')
        ok = json.dumps({'blob': 'x' * 70000}).encode()
        out, _ = self.tool('mac_cua', {'mac': 'hub', 'tool': 'get_window_state'}, Fake(Result(ok)))
        self.assertEqual(out['result'], {'blob': 'x' * 70000})

    def test_unknown_mac(self):
        out, fake = self.tool('mac_cua', {'mac': 'zzz', 'tool': 'x'})
        self.assertEqual((fake.calls, out['error']['code']), ([], 'unknown_mac'))

    def test_nonzero_exit_is_structured(self):
        out, _ = self.tool('mac_cua', {'mac': 'hub', 'tool': 'click'},
                           Fake(Result(b'', b'tool "click" is not allowed by the YAML policy\n', 1)))
        self.assertFalse(out['ok'])
        self.assertEqual(out['error']['code'], 'cua_failed')
        self.assertIn('not allowed', out['error']['detail'])

    def test_ssh_failure_and_timeout(self):
        out, _ = self.tool('mac_cua', {'mac': 'agent-mac', 'tool': 'x'}, Fake(Result(b'', b'Connection refused\n', 255)))
        self.assertEqual(out['error']['code'], 'ssh_failed')
        out, _ = self.tool('mac_cua', {'mac': 'agent-mac', 'tool': 'x'}, Fake(exc=subprocess.TimeoutExpired('ssh', 30)))
        self.assertEqual(out['error']['code'], 'timeout')

    def test_plain_text_output(self):
        out, _ = self.tool('mac_cua', {'mac': 'hub', 'tool': 'x'}, Fake(Result(b'done\n')))
        self.assertEqual(out['result'], 'done')


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
        self.assertEqual(macs.names(cfg), ['hub', 'agent-mac'])


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

    def test_seven_tools_with_required_mac(self):
        tools = self.register({'LINA_HELPER_MACS_FILE': '/nonexistent/macs.yaml'})
        self.assertEqual(sorted(tools), sorted(
            ['mac_status', 'mac_screenshot', 'mac_ax_tree', 'mac_ax_press', 'mac_click', 'mac_keys', 'mac_cua']))
        for name, t in tools.items():
            s = t['schema']
            self.assertEqual(s['name'], name)
            self.assertEqual(s['parameters']['type'], 'object')
            self.assertIn('mac', s['parameters']['required'])
            self.assertEqual(s['parameters']['properties']['mac']['type'], 'string')
            self.assertNotIn('enum', s['parameters']['properties']['mac'])
            self.assertEqual(t['toolset'], 'lina_helper')
            self.assertTrue(callable(t['handler']))
        self.assertEqual(tools['mac_cua']['schema']['parameters']['properties']['tool']['pattern'], '^[a-z_]{1,64}$')

    def test_enum_lists_names_only(self):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        p = os.path.join(d.name, 'macs.yaml')
        with open(p, 'w') as f:
            json.dump({'macs': {'hub': {'host': 'local'}, 'agent-mac': {'host': 'secret-host.example'}}}, f)
        tools = self.register({'LINA_HELPER_MACS_FILE': p})
        for t in tools.values():
            self.assertEqual(t['schema']['parameters']['properties']['mac']['enum'], ['hub', 'agent-mac'])
            self.assertNotIn('secret-host', json.dumps(t['schema']))

    def test_handler_accepts_dispatch_kwargs_and_returns_json_string(self):
        tools = self.register({'LINA_HELPER_MACS_FILE': '/nonexistent/macs.yaml'})
        out = tools['mac_status']['handler']({'mac': 'x'}, task_id='t')
        self.assertFalse(json.loads(out)['ok'])

    def test_manifest_lists_the_tools(self):
        with open(os.path.join(PLUGIN, 'plugin.yaml')) as f:
            text = f.read()
        self.assertIn('name: lina-helper', text)
        self.assertIn('kind: standalone', text)
        for name in ['mac_status', 'mac_screenshot', 'mac_ax_tree', 'mac_ax_press', 'mac_click', 'mac_keys', 'mac_cua']:
            self.assertIn('  - ' + name, text)


if __name__ == '__main__':
    unittest.main()
