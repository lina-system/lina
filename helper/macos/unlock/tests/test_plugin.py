import importlib.util
import json
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
ROOT=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('unlock_test_plugin',ROOT/'__init__.py',submodule_search_locations=[str(ROOT)])
plugin=importlib.util.module_from_spec(spec); sys.modules[spec.name]=plugin; spec.loader.exec_module(plugin)
class Context:
    def __init__(self): self.tools={}
    def register_tool(self,**kwargs): self.tools[kwargs['name']]=kwargs
class PluginTests(unittest.TestCase):
    def test_register_is_offline_and_handlers_are_bound(self):
        ctx=Context()
        with patch.object(plugin,'invoke',return_value=(0,{'result':'ok'})) as invoke:
            plugin.register(ctx); invoke.assert_not_called()
            self.assertEqual(set(ctx.tools),{'mac_unlock_status','mac_unlock','mac_unlock_revoke'})
            with patch.dict(os.environ,{'HERMES_UNLOCK_SSH_HOST':'test-mac'}):
                for name,op in [('mac_unlock_status','status'),('mac_unlock','unlock'),('mac_unlock_revoke','revoke')]:
                    tool=ctx.tools[name]
                    self.assertTrue(tool['check_fn']())
                    self.assertTrue(json.loads(tool['handler']({}))['success'])
                    self.assertEqual(invoke.call_args.args,('test-mac',op))
    def test_model_cannot_select_another_host(self):
        ctx=Context();plugin.register(ctx)
        with patch.object(plugin,'invoke') as invoke:
            result=json.loads(ctx.tools['mac_unlock']['handler']({'host':'other'}))
            self.assertFalse(result['success']);invoke.assert_not_called()
if __name__=='__main__': unittest.main()
