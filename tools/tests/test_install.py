import json
import os
import plistlib
import stat
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import install  # noqa: E402
from install import APP, PREVIOUS, STAGED, STATE, POLICY, AGENT_PLIST, CTL_LINK, SUPPORT  # noqa: E402
from sign import requirement  # noqa: E402

TEAM = 'TEAMID0000'


def make_bundle(dest, version='1.0', team=TEAM, identifier='app.lina-os.helper', adhoc=False,
                bundle_id='app.lina-os.helper', req=None, ctl_identifier='app.lina-os.helper.ctl'):
    dest = Path(dest)
    (dest / 'Contents/MacOS').mkdir(parents=True)
    (dest / 'Contents/Helpers').mkdir(parents=True)
    (dest / 'Contents/MacOS/lina-helper').write_text('exe')
    (dest / 'Contents/Helpers/lina-helperctl').write_text('ctl')
    (dest / 'Contents/Info.plist').write_bytes(plistlib.dumps(
        {'CFBundleIdentifier': bundle_id, 'CFBundleShortVersionString': version}))
    sig = {'identifier': identifier, 'adhoc': adhoc, 'team': None if adhoc else team,
           'requirement': req or ('cdhash H"%s"' % version if adhoc else requirement(team)),
           'ctl_identifier': ctl_identifier}
    (dest / 'Contents/sig.json').write_text(json.dumps(sig))
    return dest


class FakeBackend(install.Backend):
    """Real file operations on a temp root; codesign, launchctl, chown and identity are fake."""

    def __init__(self, root):
        super().__init__(root)
        self.loaded = False
        self.perm_results = []  # successive `permissions` outputs; last one repeats

    def is_root(self):
        return True

    def uid_of(self, user):
        return 501

    def chown(self, path, owner, group):
        self.log('chown', path, owner, group)

    def chown_tree(self, path, owner, group):
        self.log('chown_tree', path, owner, group)

    def sleep(self, seconds):
        pass

    def _sig(self, path):
        bundle = self.p(path)
        ctl = path.endswith('lina-helperctl')
        if ctl:
            bundle = os.path.dirname(os.path.dirname(os.path.dirname(bundle)))
        sig = json.loads(Path(bundle, 'Contents/sig.json').read_text())
        return sig, ctl

    def codesign_verify(self, path):
        self._sig(path)

    def codesign_info(self, path):
        sig, ctl = self._sig(path)
        ident = sig['ctl_identifier'] if ctl else sig['identifier']
        return install.Info(ident, sig['adhoc'], sig['team'], sig['requirement'])

    def codesign_check_requirement(self, path, req):
        sig, _ = self._sig(path)
        return not sig['adhoc'] and sig['requirement'] == req

    def launchctl(self, *args):
        self.log('launchctl', *args)
        if args[0] == 'bootstrap':
            self.loaded = True
        elif args[0] == 'bootout':
            self.loaded = False
        return (0 if args[0] != 'print' or self.loaded else 113), ''

    def run_as_user(self, user, uid, argv):
        self.log('run_as_user', user, *argv)
        if not self.perm_results:
            return 1, ''
        out = self.perm_results.pop(0) if len(self.perm_results) > 1 else self.perm_results[0]
        return (1, '') if out is None else (0, json.dumps(out))


class InstallTestCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name, 'root')
        self.root.mkdir()
        self.src = Path(self.tmp.name, 'src')
        self.src.mkdir()
        self.b = FakeBackend(str(self.root))
        self.n = 0

    def candidate(self, **kw):
        self.n += 1
        return make_bundle(self.src / ('c%d/LINA Helper.app' % self.n), **kw)

    def path(self, logical):
        return Path(self.b.p(logical))

    def version(self, logical):
        return plistlib.loads((self.path(logical) / 'Contents/Info.plist').read_bytes())['CFBundleShortVersionString']

    def install(self, **kw):
        return install.cmd_install(self.b, 'agent', self.candidate(**kw), TEAM, False)

    def event_index(self, event):
        return self.b.events.index(event)


class InstallTests(InstallTestCase):
    def test_install_creates_everything(self):
        self.install()
        self.assertEqual(self.version(APP), '1.0')
        agent = plistlib.loads(self.path(AGENT_PLIST).read_bytes())
        self.assertEqual(agent, {'Label': 'app.lina-os.helper', 'ProgramArguments': [APP + '/Contents/MacOS/lina-helper', 'serve'],
                                 'KeepAlive': True, 'RunAtLoad': True, 'LimitLoadToSessionType': 'Aqua',
                                 'ProcessType': 'Interactive'})
        self.assertTrue(self.path(CTL_LINK).is_symlink())
        self.assertEqual(os.readlink(self.path(CTL_LINK)), APP + '/Contents/Helpers/lina-helperctl')
        self.assertIn(('launchctl', 'bootstrap', 'gui/501', AGENT_PLIST), self.b.events)
        self.assertFalse(self.path('/Applications/.lina-helper-staging').exists())

    def test_policy_file_mode_and_owner_request(self):
        self.install()
        self.assertEqual(stat.S_IMODE(self.path(POLICY).stat().st_mode), 0o644)
        self.assertIn(('chown', POLICY + '.tmp-lina', 'root', 'wheel'), self.b.events)
        policy = json.loads(self.path(POLICY).read_text())
        self.assertEqual(sorted(policy['actions']), ['app.open', 'ax.press', 'ax.tree', 'input.click',
                                                     'input.keys', 'screenshot'])
        self.assertTrue(all(isinstance(v, dict) for v in policy['actions'].values()))
        self.assertEqual(policy['actions']['ax.tree'], {'max_depth': 8, 'max_nodes': 2000})
        self.assertEqual(policy['actions']['screenshot']['timeout_s'], 15)

    def test_state_is_root_only_and_lists_created_paths(self):
        self.install()
        self.assertEqual(stat.S_IMODE(self.path(STATE).stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.path('/var/db/lina-helper').stat().st_mode), 0o700)
        self.assertIn(('chown', STATE + '.tmp-lina', 'root', 'wheel'), self.b.events)
        paths = {e['path'] for e in json.loads(self.path(STATE).read_text())['created']}
        self.assertTrue({APP, AGENT_PLIST, POLICY, CTL_LINK} <= paths)

    def test_refuses_ad_hoc(self):
        with self.assertRaisesRegex(install.InstallError, 'ad-hoc'):
            self.install(adhoc=True)
        self.assertFalse(self.path(APP).exists())
        self.assertFalse(self.path(STATE).exists())
        self.assertFalse(self.path('/Applications/.lina-helper-staging').exists())

    def test_ad_hoc_allowed_for_lab(self):
        install.cmd_install(self.b, 'agent', self.candidate(adhoc=True), None, True)
        self.assertTrue(self.path(APP).is_dir())

    def test_refuses_wrong_bundle_id(self):
        with self.assertRaisesRegex(install.InstallError, 'bundle ID'):
            self.install(bundle_id='com.example.other')
        self.assertFalse(self.path(APP).exists())

    def test_refuses_wrong_signing_identifier(self):
        with self.assertRaisesRegex(install.InstallError, 'identifier'):
            self.install(identifier='com.example.other')

    def test_refuses_other_team(self):
        with self.assertRaisesRegex(install.InstallError, 'requirement'):
            self.install(team='ABCDE12345')

    def test_install_requires_team_and_refuses_reinstall(self):
        with self.assertRaisesRegex(install.InstallError, '--team'):
            install.cmd_install(self.b, 'agent', self.candidate(), None, False)
        self.install()
        with self.assertRaisesRegex(install.InstallError, 'already installed'):
            self.install()


class LaunchdTimingTests(InstallTestCase):
    def test_bootout_waits_until_launchd_drops_the_service(self):
        b = self.b
        prints = iter([0, 0, 113])
        real = b.launchctl

        def launchctl(*args):
            if args[0] == 'print':
                b.log('launchctl', *args)
                return next(prints), ''
            return real(*args)
        b.launchctl = launchctl
        install.bootout(b, 501)
        self.assertEqual(sum(1 for e in b.events if e[:2] == ('launchctl', 'print')), 3)

    def test_bootout_gives_up_loudly(self):
        b = self.b
        real = b.launchctl
        b.launchctl = lambda *a: (b.log('launchctl', *a), (0, ''))[1] if a[0] == 'print' else real(*a)
        with self.assertRaisesRegex(install.InstallError, 'still lists'):
            install.bootout(b, 501)

    def test_installed_bundle_is_chowned_to_root(self):
        self.install()
        self.assertIn(('chown_tree', install.STAGED, 'root', 'wheel'), self.b.events)


class UpgradeTests(InstallTestCase):
    def setUp(self):
        super().setUp()
        self.install(version='1.0')
        self.b.events.clear()
        self.b.perm_results = [{'accessibility': True, 'screen_recording': True}]

    def upgrade(self, **kw):
        return install.cmd_upgrade(self.b, 'agent', self.candidate(**kw), False)

    def test_refuses_different_requirement(self):
        other = requirement('ABCDE12345')
        with self.assertRaisesRegex(install.InstallError, 'requirement'):
            # Valid Developer ID bundle for the wrong team: fails the installed team's requirement.
            self.upgrade(team='ABCDE12345', req=other)
        self.assertEqual(self.version(APP), '1.0')
        self.assertNotIn(('launchctl', 'bootout', 'gui/501/app.lina-os.helper'), self.b.events)
        self.assertFalse(self.path(PREVIOUS).exists())

    def test_refuses_requirement_string_mismatch_same_team(self):
        with self.assertRaisesRegex(install.InstallError, 'requirement'):
            self.upgrade(req=requirement(TEAM) + ' and info[x] = "y"')
        self.assertEqual(self.version(APP), '1.0')

    def test_refuses_ad_hoc_and_wrong_id(self):
        with self.assertRaisesRegex(install.InstallError, 'ad-hoc'):
            self.upgrade(adhoc=True)
        with self.assertRaisesRegex(install.InstallError, 'bundle ID'):
            self.upgrade(bundle_id='com.example.other')
        self.assertEqual(self.version(APP), '1.0')

    def test_atomic_replace_order(self):
        self.upgrade(version='2.0')
        ev = self.b.events
        bootout = ev.index(('launchctl', 'bootout', 'gui/501/app.lina-os.helper'))
        to_prev = ev.index(('rename', APP, PREVIOUS))
        into_place = ev.index(('rename', STAGED, APP))
        bootstrap = ev.index(('launchctl', 'bootstrap', 'gui/501', AGENT_PLIST))
        self.assertLess(bootout, to_prev)
        self.assertLess(to_prev, into_place)
        self.assertLess(into_place, bootstrap)
        self.assertEqual(self.version(APP), '2.0')
        self.assertEqual(self.version(PREVIOUS), '1.0')
        self.assertFalse(self.path('/Applications/.lina-helper-staging').exists())
        self.assertEqual(json.loads(self.path(STATE).read_text())['installed_version'], '2.0')

    def test_upgrade_keeps_existing_policy(self):
        self.path(POLICY).write_text('{"local": true}')
        self.upgrade(version='2.0')
        self.assertEqual(self.path(POLICY).read_text(), '{"local": true}')

    def test_permissions_change_reports_failure_and_keeps_previous(self):
        self.b.perm_results = [{'accessibility': True, 'screen_recording': True},
                               {'accessibility': False, 'screen_recording': True}]
        # install had no ctl output; first call is "before", the rest come after bootstrap
        with self.assertRaisesRegex(install.InstallError, 'permissions differ'):
            self.upgrade(version='2.0')
        self.assertEqual(self.version(PREVIOUS), '1.0')

    def test_rollback_restores_previous_the_same_way(self):
        self.upgrade(version='2.0')
        self.b.events.clear()
        install.cmd_rollback(self.b, 'agent')
        self.assertEqual(self.version(APP), '1.0')
        self.assertEqual(self.version(PREVIOUS), '2.0')
        ev = self.b.events
        order = [ev.index(('launchctl', 'bootout', 'gui/501/app.lina-os.helper')),
                 ev.index(('rename', PREVIOUS, STAGED)),
                 ev.index(('rename', APP, PREVIOUS)),
                 ev.index(('rename', STAGED, APP)),
                 ev.index(('launchctl', 'bootstrap', 'gui/501', AGENT_PLIST))]
        self.assertEqual(order, sorted(order))
        self.assertEqual(json.loads(self.path(STATE).read_text())['installed_version'], '1.0')

    def test_unavailable_baseline_refuses_before_any_change(self):
        self.b.perm_results = [None]
        with self.assertRaisesRegex(install.InstallError, 'did not answer'):
            self.upgrade(version='2.0')
        self.assertEqual(self.version(APP), '1.0')
        self.assertNotIn(('launchctl', 'bootout', 'gui/501/app.lina-os.helper'), self.b.events)

    def test_new_helper_silent_is_failure(self):
        self.b.perm_results = [{'accessibility': True, 'screen_recording': True}, None]
        with self.assertRaisesRegex(install.InstallError, 'did not answer'):
            self.upgrade(version='2.0')
        self.assertEqual(self.version(PREVIOUS), '1.0')

    def test_skip_grant_check_is_explicit(self):
        self.b.perm_results = [None]
        msg = install.cmd_upgrade(self.b, 'agent', self.candidate(version='2.0'), False, True)
        self.assertIn('skipped', msg)
        self.assertEqual(self.version(APP), '2.0')

    def test_upgrade_bootout_timeout_keeps_existing_backup(self):
        self.upgrade(version='2.0')
        real = self.b.launchctl
        self.b.launchctl = lambda *a: (0, '') if a[0] == 'print' else real(*a)
        with self.assertRaisesRegex(install.InstallError, 'still lists'):
            self.upgrade(version='3.0')
        self.assertEqual(self.version(APP), '2.0')
        self.assertEqual(self.version(PREVIOUS), '1.0')

    def test_rollback_bootout_timeout_keeps_both_bundles(self):
        self.upgrade(version='2.0')
        real = self.b.launchctl
        self.b.launchctl = lambda *a: (0, '') if a[0] == 'print' else real(*a)
        with self.assertRaisesRegex(install.InstallError, 'still lists'):
            install.cmd_rollback(self.b, 'agent')
        self.assertEqual(self.version(APP), '2.0')
        self.assertEqual(self.version(PREVIOUS), '1.0')

    def test_rollback_without_previous(self):
        with self.assertRaisesRegex(install.InstallError, 'no previous'):
            install.cmd_rollback(self.b, 'agent')


class StatusRemoveTests(InstallTestCase):
    def test_status_json(self):
        self.install()
        self.b.perm_results = [{'accessibility': True}]
        out = json.loads(install.cmd_status(self.b, 'agent'))
        self.assertTrue(out['installed'])
        self.assertEqual(out['version'], '1.0')
        self.assertEqual(out['requirement'], requirement(TEAM))
        self.assertTrue(out['agent_loaded'])
        self.assertEqual(out['permissions'], {'accessibility': True})
        self.assertIn(('run_as_user', 'agent', CTL_LINK, 'permissions'), self.b.events)

    def test_status_not_installed(self):
        out = json.loads(install.cmd_status(self.b, 'agent'))
        self.assertFalse(out['installed'])
        self.assertFalse(out['agent_loaded'])

    def test_remove_deletes_only_recorded_paths(self):
        self.path(SUPPORT).mkdir(parents=True)
        self.path(POLICY).write_text('{"mine": 1}')  # existed before install: not ours
        (self.path(SUPPORT) / 'notes.txt').write_text('keep')
        self.path('/usr/local/bin').mkdir(parents=True)
        (self.path('/usr/local/bin') / 'other').write_text('keep')
        self.install()
        self.assertEqual(self.path(POLICY).read_text(), '{"mine": 1}')
        # A tampered state entry outside the known set must be ignored.
        victim = self.path('/etc/passwd')
        victim.parent.mkdir(parents=True)
        victim.write_text('x')
        state = json.loads(self.path(STATE).read_text())
        state['created'].append({'path': '/etc/passwd', 'kind': 'file'})
        self.path(STATE).write_text(json.dumps(state))

        install.cmd_remove(self.b, 'agent')

        for gone in (APP, AGENT_PLIST, CTL_LINK, STATE):
            self.assertFalse(self.path(gone).exists() or self.path(gone).is_symlink(), gone)
        self.assertEqual(self.path(POLICY).read_text(), '{"mine": 1}')
        self.assertEqual((self.path(SUPPORT) / 'notes.txt').read_text(), 'keep')
        self.assertTrue((self.path('/usr/local/bin') / 'other').exists())
        self.assertEqual(victim.read_text(), 'x')
        self.assertFalse(any(e[0] == 'launchctl' and 'tccutil' in ' '.join(map(str, e)) for e in self.b.events))

    def test_remove_cleans_dirs_it_created_and_previous(self):
        self.install()
        self.b.perm_results = [{'accessibility': True}]
        install.cmd_upgrade(self.b, 'agent', self.candidate(version='2.0'), False)
        install.cmd_remove(self.b, 'agent')
        for gone in (SUPPORT, '/var/db/lina-helper', PREVIOUS):
            self.assertFalse(self.path(gone).exists(), gone)

    def test_remove_without_state_does_nothing(self):
        make_bundle(self.path(APP))
        with self.assertRaises(install.InstallError):
            install.cmd_remove(self.b, 'agent')
        self.assertTrue(self.path(APP).is_dir())


if __name__ == '__main__':
    unittest.main()
