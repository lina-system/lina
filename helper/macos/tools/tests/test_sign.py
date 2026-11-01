import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sign  # noqa: E402
from test_install import make_bundle  # noqa: E402

TEAM = 'TEAM123456'
IDENTITY = 'Developer ID Application: Example Name (%s)' % TEAM


class SignTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.app = make_bundle(Path(tmp.name) / 'LINA Helper.app')
        self.calls = []

    def run_(self, cmd, check=True):
        self.calls.append(cmd)
        return subprocess.CompletedProcess(cmd, 0)

    def test_requirement_matches_design(self):
        self.assertEqual(sign.requirement(TEAM),
                         'identifier "app.lina-os.helper" and anchor apple generic and '
                         'certificate 1[field.1.2.840.113635.100.6.2.6] and '
                         'certificate leaf[field.1.2.840.113635.100.6.1.13] and '
                         'certificate leaf[subject.OU] = "TEAM123456"')

    def test_signs_ctl_before_app_and_verifies_with_equals_prefix(self):
        sign.sign(self.app, IDENTITY, TEAM, run=self.run_)
        signs = [c for c in self.calls if '--force' in c]
        self.assertEqual([c[c.index('--identifier') + 1] for c in signs],
                         ['app.lina-os.helper.ctl', 'app.lina-os.helper'])
        self.assertTrue(signs[0][-1].endswith('Contents/Helpers/lina-helperctl'))
        for c in signs:
            self.assertIn('runtime', c)
            self.assertIn('--timestamp', c)
        verifies = [c for c in self.calls if '-R' in c]
        self.assertEqual(len(verifies), 2)
        self.assertTrue(all(c[c.index('-R') + 1].startswith('=identifier "app.lina-os.helper') for c in verifies))
        # inside-out: sign ctl, verify ctl, sign app, verify app
        self.assertEqual(self.calls.index(signs[0]) < self.calls.index(signs[1]), True)
        self.assertIn('--deep', self.calls[-1])

    def test_refuses_non_developer_id_identity(self):
        for ident in ('Apple Development: Example (TEAM123456)', '-', 'Developer ID Installer: X (TEAM123456)',
                      'Developer ID Application: Example Name'):
            with self.assertRaises(sign.SignError, msg=ident):
                sign.sign(self.app, ident, TEAM, run=self.run_)
        self.assertEqual(self.calls, [])

    def test_refuses_bad_or_mismatching_team(self):
        for team in ('team123456', 'TEAM12345', 'TEAM1234567', 'TEAM-23456'):
            with self.assertRaises(sign.SignError, msg=team):
                sign.sign(self.app, IDENTITY, team, run=self.run_)
        with self.assertRaises(sign.SignError):
            sign.sign(self.app, 'Developer ID Application: X (ABCDE12345)', TEAM, run=self.run_)

    def test_refuses_other_bundle_id(self):
        with tempfile.TemporaryDirectory() as tmp:
            other = make_bundle(Path(tmp) / 'X.app', bundle_id='com.example.x')
            with self.assertRaisesRegex(sign.SignError, 'bundle ID'):
                sign.sign(other, IDENTITY, TEAM, run=self.run_)
        self.assertEqual(self.calls, [])

    def test_preserves_upstream_driver_and_refuses_missing_driver(self):
        driver = self.app / 'Contents/Helpers/cua-driver'
        driver.write_bytes(b'upstream signed bytes')
        sign.sign(self.app, IDENTITY, TEAM, run=self.run_)
        self.assertEqual(driver.read_bytes(), b'upstream signed bytes')
        self.assertFalse(any('--sign' in c and (c[-1] == str(driver) or '--deep' in c) for c in self.calls))
        driver.unlink()
        self.calls.clear()
        with self.assertRaisesRegex(sign.SignError, 'cua-driver'):
            sign.sign(self.app, IDENTITY, TEAM, run=self.run_)
        self.assertEqual(self.calls, [])


if __name__ == '__main__':
    unittest.main()
