import importlib.util
import os
import sys
import types
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
PKG = os.path.join(os.path.dirname(HERE), 'hermes-computer-use')


def load(backend_cls):
    """Import the provider with stub Hermes modules (no Hermes install needed)."""
    backend_mod = types.ModuleType('tools.computer_use.backend')
    backend_mod.ComputerUseBackend = object
    backend_mod.ComputerUseProvider = object
    cua_mod = types.ModuleType('tools.computer_use.cua_backend')
    cua_mod.CuaDriverBackend = backend_cls
    mods = {'tools': types.ModuleType('tools'), 'tools.computer_use': types.ModuleType('tools.computer_use'),
            'tools.computer_use.backend': backend_mod, 'tools.computer_use.cua_backend': cua_mod}
    with mock.patch.dict(sys.modules, mods):
        spec = importlib.util.spec_from_file_location('lina_helper_cua', os.path.join(PKG, '__init__.py'))
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    return mod, mods


class ProviderTests(unittest.TestCase):
    def setUp(self):
        self.made = []
        made = self.made

        class FakeBackend:
            def __init__(self, permission_mode='standard'):
                made.append(permission_mode)
        self.mod, self.mods = load(FakeBackend)

    def backend(self, mode, env):
        with mock.patch.dict(sys.modules, self.mods), mock.patch.dict(os.environ, env, clear=True):
            self.mod.LinaHelperCuaProvider().create_backend(permission_mode=mode)
            return dict(os.environ)

    def test_every_requested_mode_becomes_standard(self):
        for mode in ('standard', 'bounded', 'unrestricted'):
            self.backend(mode, {})
        self.assertEqual(self.made, ['standard'] * 3)

    def test_wrapper_is_the_driver_command_unless_overridden(self):
        env = self.backend('unrestricted', {})
        self.assertEqual(env['HERMES_CUA_DRIVER_CMD'], os.path.join(os.path.dirname(PKG), 'hermes-cua-driver'))
        env = self.backend('standard', {'HERMES_CUA_DRIVER_CMD': '/x/driver'})
        self.assertEqual(env['HERMES_CUA_DRIVER_CMD'], '/x/driver')

    def test_register_and_name(self):
        got = []
        ctx = types.SimpleNamespace(register_computer_use_provider=got.append)
        self.mod.register(ctx)
        self.assertEqual(got[0].name, 'lina-helper-cua')
        with open(os.path.join(PKG, 'plugin.yaml')) as f:
            self.assertIn('name: lina-helper-cua', f.read())


if __name__ == '__main__':
    unittest.main()
