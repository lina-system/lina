import copy
import sys
import unittest
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'tools'))
from policy import Policy, Conflict, SCREEN, REMOTE, ORIGINAL, WRAPPER

class Fake:
    def __init__(self):
        self.rules = {SCREEN: {'class': 'rule', 'rule': ['custom-a', 'custom-b'], 'k-of-n': 2, 'created': 123}}
        self.initial = copy.deepcopy(self.rules[SCREEN])
        self.state = {'phase': 'staged'}
        self.clock = 10
        self.boot_id = 'boot-1'
        self.fail = None
        self.locked = True
        self.success = True
    def load(self): return copy.deepcopy(self.state)
    def save(self, state): self.state = copy.deepcopy(state)
    def now(self): return self.clock
    def boot(self): return self.boot_id
    def preflight(self): pass
    def read_right(self, name): return copy.deepcopy(self.rules.get(name))
    def write_right(self, name, value):
        if self.fail == name:
            self.fail = None
            raise OSError('injected write failure')
        self.rules[name] = copy.deepcopy(value)
    def remove_right(self, name): self.rules.pop(name, None)
    def status(self): return {'screen_locked': self.locked}
    def unlock(self):
        return {'result': 'unlocked', 'desktop_ready': self.success, 'screen_unlocked': True,
                'authorization_consumed': True, 'legacy_keychain_status': 0, 'data_protection_status': 0}

class PolicyTests(unittest.TestCase):
    def test_preserves_entire_original_instead_of_flattening_threshold(self):
        b=Fake(); p=Policy(b); p.activate()
        self.assertEqual(b.rules[ORIGINAL], b.initial)
        self.assertEqual(b.rules[SCREEN], WRAPPER)
        p.rollback(); self.assertEqual(b.rules, {SCREEN:b.initial})
    def test_each_partial_write_rolls_back(self):
        for name in [ORIGINAL,REMOTE,SCREEN]:
            with self.subTest(name=name):
                b=Fake(); b.fail=name
                with self.assertRaises(OSError): Policy(b).activate()
                self.assertEqual(b.rules, {SCREEN:b.initial})
                self.assertEqual(b.state['phase'],'staged')
    def test_deadline_and_reboot_rollback(self):
        for reboot in [False, True]:
            b=Fake(); p=Policy(b); p.activate()
            if reboot: b.boot_id='boot-2'
            else: b.clock=130
            p.expire(); self.assertEqual(b.rules,{SCREEN:b.initial})
    def test_crash_after_rule_write_is_recoverable(self):
        b=Fake(); p=Policy(b); p.activate(); b.state['phase']='activating'; b.clock=999
        p.expire(); self.assertEqual(b.rules,{SCREEN:b.initial})
    def test_drift_never_overwrites_unrelated_changes(self):
        b=Fake(); p=Policy(b); p.activate(); changed={'class':'user','group':'admin'}
        b.rules[SCREEN]=changed
        with self.assertRaises(Conflict): p.rollback()
        self.assertEqual(b.rules[SCREEN],changed)
        self.assertIn(REMOTE,b.rules)
    def test_reserved_name_collision_aborts_before_mutation(self):
        b=Fake(); b.rules[REMOTE]={'class':'deny'}
        with self.assertRaises(Conflict): Policy(b).activate()
        self.assertEqual(b.rules[SCREEN],b.initial)
    def test_reserved_reference_cannot_create_authorization_cycle(self):
        b=Fake(); b.rules[SCREEN]={'class':'rule','rule':[ORIGINAL]}
        with self.assertRaises(Conflict): Policy(b).activate()
        self.assertNotIn(ORIGINAL,b.rules)
    def test_confirm_requires_observed_transition_and_keychains(self):
        b=Fake(); p=Policy(b); p.activate()
        with self.assertRaises(Conflict): p.confirm()
        b.locked=False
        with self.assertRaises(Conflict): p.verify()
        b.locked=True; b.success=False
        with self.assertRaises(Conflict): p.verify()
        b.success=True; p.verify(); p.confirm()
        b.clock=1000; p.expire(); self.assertEqual(b.state['phase'],'confirmed')
    def test_expired_trial_cannot_be_confirmed(self):
        b=Fake(); p=Policy(b); p.activate(); p.verify(); b.clock=999
        with self.assertRaises(Conflict): p.confirm()
        self.assertEqual(b.rules,{SCREEN:b.initial})
    def test_confirmation_preflight_cannot_outlive_trial(self):
        b=Fake(); p=Policy(b); p.activate(); p.verify()
        b.preflight=lambda: setattr(b,'clock',999)
        with self.assertRaises(Conflict): p.confirm()
        self.assertEqual(b.rules,{SCREEN:b.initial})
    def test_invalid_timeout(self):
        for seconds in [0,29,601]:
            with self.assertRaises(Conflict): Policy(Fake()).activate(seconds)

if __name__ == '__main__': unittest.main()
