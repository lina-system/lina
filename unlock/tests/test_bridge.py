import json
import subprocess
import sys
import unittest
from pathlib import Path
sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'bridge'))
from mac_unlock import command,invoke
class BridgeTests(unittest.TestCase):
    def runner(self,value,code=0):
        def run(argv,**kwargs):
            self.assertEqual(kwargs['stdin'],subprocess.DEVNULL)
            self.assertEqual(kwargs['timeout'],25)
            return subprocess.CompletedProcess(argv,code,json.dumps(value).encode(),b'')
        return run
    def test_no_shell_or_option_injection(self):
        for host in ['-oProxyCommand=bad','host;touch bad','user@host','$(id)','a b']:
            with self.assertRaises(ValueError): command(host,'unlock')
        argv=command('mac-test','unlock')
        self.assertIn('BatchMode=yes',argv)
        self.assertTrue(argv[-1].startswith("sudo -n '/Library/Application Support/"))
    def test_success_requires_both_keychains_and_receipt(self):
        v={'schema_version':1,'result':'unlocked','desktop_ready':True,'screen_unlocked':True,
           'authorization_consumed':True,'legacy_keychain_status':0,'data_protection_status':0,'active_displays':1}
        self.assertEqual(invoke('mac-test','unlock',self.runner(v))[0],0)
        for key,bad in [('data_protection_status',-25308),('authorization_consumed',False),('active_displays',0),
                        ('active_displays','1'),('legacy_keychain_status',False)]:
            changed={**v,key:bad}
            code,result=invoke('mac-test','unlock',self.runner(changed))
            self.assertEqual(code,1); self.assertFalse(result['desktop_ready'])
    def test_nonzero_transport_exit_cannot_be_success(self):
        code,_=invoke('mac-test','status',self.runner({'schema_version':1,'result':'status'},255))
        self.assertEqual(code,1)
    def test_timeout_has_no_retry(self):
        calls=[]
        def run(argv,**kwargs): calls.append(argv); raise subprocess.TimeoutExpired(argv,25)
        code,v=invoke('mac-test','unlock',run)
        self.assertEqual(len(calls),1);self.assertEqual(code,1);self.assertEqual(v['result'],'transport_timeout')
if __name__=='__main__': unittest.main()
