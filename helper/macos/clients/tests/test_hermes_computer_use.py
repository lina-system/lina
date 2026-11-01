import importlib.util
import os
import sys
import types
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
PKG = os.path.join(os.path.dirname(HERE), 'hermes-computer-use')


class ActionResult:
    def __init__(self, ok, action, message='', code=None, **kw):
        self.ok, self.action, self.message, self.code = ok, action, message, code


class FakeSession:
    def __init__(self):
        self._embedded_daemon = None


class FakeCuaBackend:
    """Stands in for upstream CuaDriverBackend: records what the subclass passes and calls."""
    made = []

    def __init__(self, permission_mode='standard'):
        FakeCuaBackend.made.append(permission_mode)
        self._embedded_daemon = None
        self._session = FakeSession()
        self._session_id = 'hermes-abc'
        self.calls = []

    def start(self):
        self.calls.append(('start', os.environ.get('HERMES_CUA_DRIVER_CMD')))

    def stop(self):
        self.calls.append(('stop',))

    def click(self, **kw):
        self.calls.append(('click', kw))
        return ActionResult(True, 'click')

    def type_text(self, text, **kw):
        self.calls.append(('type_text', text))
        return ActionResult(True, 'type_text')

    def capture(self, **kw):
        self.calls.append(('capture', kw))
        return 'capture'


def load():
    backend_mod = types.ModuleType('tools.computer_use.backend')
    backend_mod.ComputerUseBackend = object
    backend_mod.ComputerUseProvider = object
    backend_mod.ActionResult = ActionResult
    cua_mod = types.ModuleType('tools.computer_use.cua_backend')
    cua_mod.CuaDriverBackend = FakeCuaBackend
    cua_mod.cua_driver_child_env = lambda: {'PATH': '/usr/bin', 'LINA_HELPER_CUA_HOST': 'stale'}
    mods = {'tools': types.ModuleType('tools'), 'tools.computer_use': types.ModuleType('tools.computer_use'),
            'tools.computer_use.backend': backend_mod, 'tools.computer_use.cua_backend': cua_mod}
    with mock.patch.dict(sys.modules, mods):
        spec = importlib.util.spec_from_file_location('lina_helper_cua_test', os.path.join(PKG, '__init__.py'))
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    return mod, mods


class FakeCore:
    def __init__(self, lease=None):
        self.targets = types.SimpleNamespace(get=lambda sid: 'agent-mac' if sid == 'remote' else 'agent-hub',
                                             current_session_id=lambda: 'sess')
        self.actions = []
        self.lease = lease or {'ok': True, 'result': {'holder': 'hermes-abc', 'expires_in_s': 300}}

    def resolve_host(self, mac):
        return 'agent-mac' if mac == 'agent-mac' else 'local'

    def helper_action(self, mac, action, args):
        self.actions.append((mac, action, args))
        return self.lease if action == 'cua.lease' else {'ok': True, 'result': {}}


class ProviderTests(unittest.TestCase):
    def setUp(self):
        FakeCuaBackend.made = []
        self.mod, self.mods = load()
        patch = mock.patch.dict(sys.modules, self.mods)
        patch.start()
        self.addCleanup(patch.stop)
        self.cls = self.mod.make_backend_class()

    def test_mode_is_always_standard(self):
        for mode in ('standard', 'bounded', 'unrestricted'):
            with mock.patch.object(self.mod, 'plugin_core', return_value=FakeCore()):
                self.mod.LinaHelperCuaProvider().create_backend(permission_mode=mode)
        self.assertEqual(FakeCuaBackend.made, ['standard'] * 3)

    def test_endpoint_points_the_session_at_the_wrapper_and_the_mac(self):
        local = self.cls(session_id='sess', core=FakeCore())
        self.assertIs(local._session._embedded_daemon, local._embedded_daemon)
        self.assertEqual(local._embedded_daemon.proxy_invocation(), (self.mod.WRAPPER, ['mcp']))
        self.assertNotIn('LINA_HELPER_CUA_HOST', local._embedded_daemon.child_env())
        remote = self.cls(session_id='remote', core=FakeCore())
        self.assertEqual(remote.mac, 'agent-mac')
        self.assertEqual(remote._embedded_daemon.child_env()['LINA_HELPER_CUA_HOST'], 'agent-mac')
        self.assertEqual(remote._embedded_daemon.child_env()['LINA_HELPER_CUA_HOLDER'], 'hermes-abc')

    def test_start_checks_the_contract_against_the_wrapper_and_restores_env(self):
        b = self.cls(session_id='sess', core=FakeCore())
        with mock.patch.dict(os.environ, {'HERMES_CUA_DRIVER_CMD': '/x'}):
            b.start()
            self.assertEqual(os.environ['HERMES_CUA_DRIVER_CMD'], '/x')
        self.assertEqual(b.calls[0], ('start', self.mod.WRAPPER))
        with mock.patch.dict(os.environ, {}, clear=True):
            b.start()
            self.assertNotIn('HERMES_CUA_DRIVER_CMD', os.environ)

    def test_input_takes_the_lease_once_reads_do_not(self):
        core = FakeCore()
        b = self.cls(session_id='sess', core=core)
        b.capture(mode='som')
        self.assertEqual(core.actions, [])
        self.assertTrue(b.click(x=1, y=2).ok)
        self.assertTrue(b.type_text('hi').ok)
        self.assertEqual(core.actions, [('agent-hub', 'cua.lease', {'holder': 'hermes-abc', 'ttl_s': 300})])
        with mock.patch.object(self.mod.time, 'monotonic', return_value=10 ** 9):
            b.click(x=1, y=2)
        self.assertEqual(len(core.actions), 2)

    def test_busy_lease_refuses_without_calling_upstream(self):
        core = FakeCore(lease={'ok': False, 'error': {'code': 'busy', 'detail': 'hermes-other is using this Mac (200s left)'}})
        b = self.cls(session_id='sess', core=core)
        r = b.click(x=1, y=2)
        self.assertFalse(r.ok)
        self.assertEqual(r.code, 'mac_busy')
        self.assertIn('hermes-other is using this Mac', r.message)
        self.assertNotIn('click', [c[0] for c in b.calls])

    def test_stop_releases_a_held_lease(self):
        core = FakeCore()
        b = self.cls(session_id='sess', core=core)
        b.stop()
        self.assertEqual(core.actions, [])
        b.click(x=1, y=2)
        b.stop()
        self.assertEqual([a[1] for a in core.actions], ['cua.lease', 'cua.release'])

    def test_register_and_name(self):
        got = []
        self.mod.register(types.SimpleNamespace(register_computer_use_provider=got.append))
        self.assertEqual(got[0].name, 'lina-helper-cua')
        with open(os.path.join(PKG, 'plugin.yaml')) as f:
            self.assertIn('name: lina-helper-cua', f.read())


if __name__ == '__main__':
    unittest.main()
