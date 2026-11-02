import contextlib
import copy
import json
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'tools'))
import installer

class Store:
    def __init__(self): self.state={}
    def save(self,value): self.state=copy.deepcopy(value)
    def load(self): return copy.deepcopy(self.state)

class InstallerTests(unittest.TestCase):
    def scenario(self,fail_bootstrap=False):
        with tempfile.TemporaryDirectory() as temp,contextlib.ExitStack() as stack:
            base=Path(temp); build=base/'build';build.mkdir()
            root=base/'installed';state=base/'state';state.mkdir()
            (build/'hermes-unlockctl').write_text('synthetic native helper')
            for name in ['Hermes Unlock Agent.app','HermesUnlockAuthorizationPlugin.bundle']:
                (build/name).mkdir();(build/name/'synthetic').write_text('test payload')
            paths={'ROOT':root,'STATE':state,'MANIFEST':state/'state.json','RUNTIME':base/'runtime',
                   'PLUGIN':base/'installed-plugin','AGENT':root/'Hermes Unlock Agent.app','CTL':root/'bin/hermes-unlockctl',
                   'LA':base/'agent.plist','PREPARE':base/'prepare.plist','WATCHDOG':base/'watchdog.plist','SUDOERS':base/'sudoers'}
            for key,value in paths.items():stack.enter_context(patch.object(installer,key,value))
            stack.enter_context(patch.object(installer.pwd,'getpwnam',return_value=SimpleNamespace(pw_uid=501)))
            stack.enter_context(patch.object(installer,'lock_down'))
            stack.enter_context(patch.object(installer,'trusted_python',return_value=Path('/synthetic/root/python3')))
            calls=[]
            def run(argv,data=None,check=True,timeout=20):
                argv=list(map(str,argv));calls.append(argv)
                if fail_bootstrap and argv[:3]==['/bin/launchctl','bootstrap','gui/501']:
                    raise RuntimeError('injected launch failure')
                return subprocess.CompletedProcess(argv,0,b'',b'')
            stack.enter_context(patch.object(installer,'run',side_effect=run))
            args=SimpleNamespace(user='testuser',build=build,allow_ad_hoc_for_lab=False,enable_ssh_command=True)
            backend=Store()
            if fail_bootstrap:
                with self.assertRaises(RuntimeError):installer.stage(args,backend)
                self.assertEqual(backend.state['phase'],'uninstalled')
                self.assertFalse(root.exists());self.assertFalse(paths['PLUGIN'].exists());self.assertFalse(paths['SUDOERS'].exists())
            else:
                installer.stage(args,backend)
                self.assertEqual(backend.state['phase'],'staged')
                self.assertEqual((root/'allowed-uid').read_text(),'501\n')
                self.assertEqual(plistlib.loads(paths['WATCHDOG'].read_bytes())['StartInterval'],15)
                sudoers=paths['SUDOERS'].read_text()
                self.assertIn('sha256:',sudoers);self.assertNotIn('*',sudoers)
                self.assertIn(' unlock',sudoers);self.assertNotIn(' activate',sudoers)
                signed=[c for c in calls if c[:3]==['/usr/bin/codesign','--verify','--strict']]
                self.assertEqual(len(signed),6)
                # A requirement without '=' is read by codesign as a file path.
                self.assertTrue(all(c[c.index('-R')+1]=='='+installer.DEVELOPER_ID for c in signed))
            self.assertTrue((build/'hermes-unlockctl').exists())
            self.assertFalse(any('authorizationdb' in c for c in calls))
    def test_stage_files_services_and_narrow_sudoers_without_authentication_mutation(self):
        self.scenario()
    def test_stage_failure_removes_only_package_paths(self):
        self.scenario(fail_bootstrap=True)
    def test_stale_atomic_temp_does_not_block_recovery(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/'state.json'
            path.with_name('state.json.new').write_text('interrupted old write')
            installer.atomic_json(path,{'phase':'staged'})
            self.assertEqual(json.loads(path.read_text()),{'phase':'staged'})
            self.assertEqual(path.stat().st_mode & 0o777,0o600)
    def test_watchdog_rejects_writable_interpreter_ancestry(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/'python3';path.write_text('synthetic interpreter')
            path.chmod(0o755);path.parent.chmod(0o777)
            with self.assertRaises(installer.Conflict):installer.trusted_python([path])
if __name__=='__main__':unittest.main()
