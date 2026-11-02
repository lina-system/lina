import json
import os
import plistlib
import stat
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import install
from install import APP, PREVIOUS, STAGED, STATE, POLICY, AGENT_PLIST, CTL_LINK, SUPPORT
from sign import requirement

TEAM = 'TEAM123456'


def make_bundle(dest, version='1.0', team=TEAM, identifier='app.lina-os.helper', adhoc=False,
                bundle_id='app.lina-os.helper', req=None, ctl_identifier='app.lina-os.helper.ctl'):
    dest = Path(dest)
    (dest / 'Contents/MacOS').mkdir(parents=True)
    (dest / 'Contents/Helpers').mkdir(parents=True)
    (dest / 'Contents/MacOS/lina-helper').write_text('exe')
    (dest / 'Contents/Helpers/lina-helperctl').write_text('ctl')
    (dest / 'Contents/Helpers/cua-driver').write_text('upstream')
    (dest / 'Contents/Info.plist').write_bytes(plistlib.dumps(
        {'CFBundleIdentifier': bundle_id, 'CFBundleShortVersionString': version}))
    sig = {'identifier': identifier, 'adhoc': adhoc, 'team': None if adhoc else team,
           'requirement': req or (f'cdhash H"{version}"' if adhoc else requirement(team)),
           'ctl_identifier': ctl_identifier}
    (dest / 'Contents/sig.json').write_text(json.dumps(sig))
    return dest


class FakeBackend(install.Backend):
    """Real file operations on a temp root; codesign, launchctl, chown and identity are fake."""

    def __init__(self, root):
        super().__init__(root)
        self.loaded = False
        self.perm_results = []  # successive `permissions` outputs; last one repeats
        self.cua_team = 'YCK386LBJ7'
        self.cua_valid = True

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
        ctl = path.endswith(('lina-helperctl', 'cua-driver'))
        if ctl:
            bundle = os.path.dirname(os.path.dirname(os.path.dirname(bundle)))
        sig = json.loads(Path(bundle, 'Contents/sig.json').read_text())
        return sig, ctl

    def codesign_verify(self, path):
        self.log('codesign_verify', path)
        if path.endswith('/cua-driver') and (not self.cua_valid or not Path(self.p(path)).is_file()):
            raise install.InstallError('invalid cua-driver signature')
        self._sig(path)

    def codesign_info(self, path):
        if path.endswith('/cua-driver'):
            return install.Info('cua-driver', False, self.cua_team, 'upstream requirement')
        sig, ctl = self._sig(path)
        ident = sig['ctl_identifier'] if ctl else sig['identifier']
        return install.Info(ident, sig['adhoc'], sig['team'], sig['requirement'])

    def codesign_check_requirement(self, path, req):
        if path.endswith('/cua-driver'):
            return self.cua_valid and self.cua_team == 'YCK386LBJ7'
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
        return make_bundle(self.src / f'c{self.n}/LINA Helper.app', **kw)

    def path(self, logical):
        return Path(self.b.p(logical))

    def version(self, logical):
        return plistlib.loads((self.path(logical) / 'Contents/Info.plist').read_bytes())['CFBundleShortVersionString']

    def install(self, **kw):
        return install.cmd_install(self.b, 'agent', self.candidate(**kw), TEAM, False)

    def test_nested_upstream_driver_verified_with_its_own_team_even_in_lab_mode(self):
        for adhoc in (False, True):
            self.b.cua_team = 'YCK386LBJ7'; self.b.cua_valid = True
            app = f'/candidate-{adhoc}.app'
            make_bundle(self.path(app), adhoc=adhoc)
            install.verify_candidate(self.b, app, TEAM, adhoc)
            self.assertIn(('codesign_verify', app + '/Contents/Helpers/cua-driver'), self.b.events)
            self.b.cua_team = TEAM
            with self.assertRaisesRegex(install.InstallError, 'cua-driver'):
                install.verify_candidate(self.b, app, TEAM, adhoc)
            self.b.cua_team = 'YCK386LBJ7'; self.b.cua_valid = False
            with self.assertRaises(install.InstallError):
                install.verify_candidate(self.b, app, TEAM, adhoc)
            self.b.cua_valid = True
            self.path(app + '/Contents/Helpers/cua-driver').unlink()
            with self.assertRaises(install.InstallError):
                install.verify_candidate(self.b, app, TEAM, adhoc)

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
        self.assertEqual(sorted(policy['actions']), ['ax.tree', 'screenshot'])
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

    def test_ad_hoc_lab_install_runs_the_helper_in_development_mode(self):
        # Without it the teamless helper rejects every client, even permissions and status (lina#60).
        install.cmd_install(self.b, 'agent', self.candidate(adhoc=True), None, True)
        agent = plistlib.loads(self.path(AGENT_PLIST).read_bytes())
        self.assertEqual(agent['EnvironmentVariables'], {'LINA_HELPER_DEV': '1'})

    def test_ad_hoc_upgrade_repairs_an_agent_without_development_mode(self):
        install.cmd_install(self.b, 'agent', self.candidate(adhoc=True, version='1.0'), None, True)
        self.path(AGENT_PLIST).write_bytes(install.agent_plist(False))  # as written before lina#60
        install.cmd_upgrade(self.b, 'agent', self.candidate(adhoc=True, version='2.0'), True, skip_grant_check=True)
        agent = plistlib.loads(self.path(AGENT_PLIST).read_bytes())
        self.assertEqual(agent['EnvironmentVariables'], {'LINA_HELPER_DEV': '1'})

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

    def test_signed_upgrade_leaves_the_agent_unchanged(self):
        before = self.path(AGENT_PLIST).read_bytes()
        self.upgrade(version='2.0')
        self.assertEqual(self.path(AGENT_PLIST).read_bytes(), before)
        self.assertNotIn('EnvironmentVariables', plistlib.loads(before))

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

    def test_rollback_accepts_a_backup_built_before_cua_driver(self):
        # The installed 1.0 predates the embedded driver; upgrading to 2.0 keeps it as the backup.
        self.path(APP + '/Contents/Helpers/cua-driver').unlink()
        self.upgrade(version='2.0')
        install.cmd_rollback(self.b, 'agent')
        self.assertEqual(self.version(APP), '1.0')
        self.assertFalse(self.path(APP + '/Contents/Helpers/cua-driver').exists())
        # A candidate for install/upgrade still must carry it.
        app = '/no-cua.app'
        make_bundle(self.path(app))
        self.path(app + '/Contents/Helpers/cua-driver').unlink()
        with self.assertRaisesRegex(install.InstallError, 'no Contents/Helpers/cua-driver'):
            install.verify_candidate(self.b, app, TEAM, False)
        # A driver that is present in a rollback target is still verified: now APP=3.0, PREVIOUS=2.0
        # after one more upgrade from 2.0 (both carry the driver).
        install.cmd_rollback(self.b, 'agent')  # back to 2.0 (has the driver)
        self.upgrade(version='3.0')
        self.assertTrue(self.path(PREVIOUS + '/Contents/Helpers/cua-driver').exists())
        self.b.cua_valid = False
        with self.assertRaisesRegex(install.InstallError, 'cua-driver'):
            install.cmd_rollback(self.b, 'agent')

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


class UserModeTests(InstallTestCase):
    def setUp(self):
        super().setUp()
        self.b.is_root = lambda: False
        self.b.current_user = lambda: ('test-admin', 501, '/Users/test-admin')
        self.user = self.b.configure_mode('user')
        self.paths = self.b.paths

    def install(self, **kw):
        return install.cmd_install(self.b, self.user, self.candidate(**kw), TEAM, False)

    def test_user_layout_ownership_and_plist(self):
        self.install()
        self.assertEqual(self.user, 'test-admin')
        self.assertEqual(self.paths.AGENT_PLIST, '/Users/test-admin/Library/LaunchAgents/app.lina-os.helper.plist')
        self.assertEqual(self.paths.STATE, '/Users/test-admin/Library/Application Support/LINA Helper/install/state.json')
        for path in [self.paths.STAGED, self.paths.PREVIOUS]:
            self.assertEqual(Path(path).parent.parent, Path('/Applications'))
        agent = plistlib.loads(self.path(self.paths.AGENT_PLIST).read_bytes())
        self.assertEqual(agent, {'Label': install.LABEL, 'ProgramArguments': [install.EXE, 'serve'],
                                'KeepAlive': True, 'RunAtLoad': True,
                                'LimitLoadToSessionType': 'Aqua', 'ProcessType': 'Interactive'})
        self.assertEqual(stat.S_IMODE(self.path(self.paths.AGENT_PLIST).stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(self.path(self.paths.STATE).stat().st_mode), 0o600)
        for path in [APP, self.paths.AGENT_PLIST, self.paths.STATE]:
            self.assertEqual(self.path(path).stat().st_uid, os.geteuid())
        self.assertIn(('launchctl', 'bootstrap', 'gui/501', self.paths.AGENT_PLIST), self.b.events)
        self.assertFalse(any(e[0] in ('chown', 'chown_tree', 'symlink') for e in self.b.events))
        for path in [POLICY, CTL_LINK, AGENT_PLIST, STATE]:
            self.assertFalse(self.path(path).exists(), path)

    def test_user_mode_rejects_root_or_other_user_before_writes(self):
        with self.assertRaisesRegex(install.InstallError, 'current user'):
            self.b.configure_mode('user', 'another-user')
        self.assertEqual(self.b.configure_mode('user', 'test-admin'), 'test-admin')
        self.b.is_root = lambda: True
        with self.assertRaisesRegex(install.InstallError, 'root'):
            self.b.configure_mode('user')
        self.assertEqual(self.b.events, [])

    def test_user_signature_checks_are_identical(self):
        for kwargs, message in [({'adhoc': True}, 'ad-hoc'), ({'team': 'ABCDE12345'}, 'requirement'),
                                ({'bundle_id': 'com.example.other'}, 'bundle ID'),
                                ({'ctl_identifier': 'com.example.other'}, 'identifier')]:
            with self.assertRaisesRegex(install.InstallError, message):
                self.install(**kwargs)
            self.assertFalse(self.path(APP).exists())

    def test_upgrade_rollback_status_remove(self):
        self.install(version='1.0')
        self.b.perm_results = [{'accessibility': True, 'screen_recording': True}]
        self.b.events.clear()
        install.cmd_upgrade(self.b, self.user, self.candidate(version='2.0'), False)
        order = [self.b.events.index(e) for e in [
            ('launchctl', 'bootout', 'gui/501/app.lina-os.helper'),
            ('rename', APP, self.paths.PREVIOUS), ('rename', self.paths.STAGED, APP),
            ('launchctl', 'bootstrap', 'gui/501', self.paths.AGENT_PLIST)]]
        self.assertEqual(order, sorted(order))
        self.assertEqual(self.version(APP), '2.0')
        self.assertEqual(self.version(self.paths.PREVIOUS), '1.0')
        self.assertIn(('run_as_user', self.user, install.CTL_IN_APP, 'permissions'), self.b.events)
        install.cmd_rollback(self.b, self.user)
        self.assertEqual(self.version(APP), '1.0')
        self.assertEqual(self.version(self.paths.PREVIOUS), '2.0')
        status = json.loads(install.cmd_status(self.b, self.user))
        self.assertTrue(status['installed'])
        self.assertTrue(status['agent_loaded'])
        self.assertTrue(status['previous'])
        self.assertEqual(status['user'], self.user)
        self.assertEqual(status['permissions'], self.b.perm_results[0])
        # Even a tampered user state cannot delete root-mode artifacts or other home files.
        self.path(POLICY).parent.mkdir(parents=True)
        self.path(POLICY).write_text('keep root policy')
        state = json.loads(self.path(self.paths.STATE).read_text())
        state['created'].append({'path': POLICY, 'kind': 'file'})
        self.path(self.paths.STATE).write_text(json.dumps(state))
        install.cmd_remove(self.b, self.user)
        for path in [APP, self.paths.PREVIOUS, self.paths.AGENT_PLIST, self.paths.STATE]:
            self.assertFalse(self.path(path).exists(), path)
        self.assertEqual(self.path(POLICY).read_text(), 'keep root policy')
        self.assertFalse(any(e[0] in ('chown', 'chown_tree', 'symlink') for e in self.b.events))

    def test_upgrade_and_rollback_compare_grants(self):
        self.install()
        self.b.perm_results = [None]
        with self.assertRaisesRegex(install.InstallError, 'did not answer'):
            install.cmd_upgrade(self.b, self.user, self.candidate(version='2.0'), False)
        self.assertEqual(self.version(APP), '1.0')
        self.b.perm_results = [{'accessibility': True}, {'accessibility': False}]
        with self.assertRaisesRegex(install.InstallError, 'permissions differ'):
            install.cmd_upgrade(self.b, self.user, self.candidate(version='2.0'), False)
        self.assertEqual(self.version(self.paths.PREVIOUS), '1.0')
        self.b.perm_results = [{'accessibility': False}, {'accessibility': True}]
        with self.assertRaisesRegex(install.InstallError, 'permissions differ'):
            install.cmd_rollback(self.b, self.user)
        self.assertEqual(self.version(APP), '1.0')

    def test_upgrade_rejects_requirement_change_before_bootout(self):
        self.install()
        self.b.events.clear()
        with self.assertRaisesRegex(install.InstallError, 'requirement'):
            install.cmd_upgrade(self.b, self.user,
                                self.candidate(req=requirement(TEAM) + ' and info[x] = "y"'), False)
        self.assertEqual(self.version(APP), '1.0')
        self.assertFalse(any(e[:2] == ('launchctl', 'bootout') for e in self.b.events))

    def test_swap_failure_restores_installed_bundle(self):
        self.install()
        self.b.perm_results = [{'accessibility': True}]
        rename = self.b.rename

        def fail_candidate(src, dst):
            if src == self.paths.STAGED and dst == APP:
                raise OSError('injected rename failure')
            return rename(src, dst)
        self.b.rename = fail_candidate
        with self.assertRaisesRegex(install.InstallError, 'previous bundle restored'):
            install.cmd_upgrade(self.b, self.user, self.candidate(version='2.0'), False)
        self.assertEqual(self.version(APP), '1.0')
        self.assertTrue(self.b.loaded)
        self.assertFalse(self.path(self.paths.STAGING_DIR).exists())

    def test_rollback_failure_preserves_both_versions(self):
        self.install()
        self.b.perm_results = [{'accessibility': True}]
        install.cmd_upgrade(self.b, self.user, self.candidate(version='2.0'), False)
        rename = self.b.rename

        def fail_previous(src, dst):
            if src == self.paths.STAGED and dst == APP:
                raise OSError('injected rename failure')
            return rename(src, dst)
        self.b.rename = fail_previous
        with self.assertRaisesRegex(install.InstallError, 'nothing changed'):
            install.cmd_rollback(self.b, self.user)
        self.assertEqual(self.version(APP), '2.0')
        self.assertEqual(self.version(self.paths.PREVIOUS), '1.0')
        self.assertTrue(self.b.loaded)

    def test_permission_probe_runs_directly_without_sudo_or_asuser(self):
        from unittest.mock import patch
        with patch.object(install.subprocess, 'run') as run:
            run.return_value.returncode = 0
            run.return_value.stdout = '{}'
            install.Backend.run_as_user(self.b, self.user, 501, [install.CTL_IN_APP, 'permissions'])
        self.assertEqual(run.call_args.args[0], [install.CTL_IN_APP, 'permissions'])


class ProfileResourceTests(unittest.TestCase):
    def test_installer_defaults_keep_explicit_read_actions(self):
        resource = Path(__file__).resolve().parents[2] / 'app/Resources/PolicyProfiles.json'
        profiles = json.loads(resource.read_text())
        self.assertEqual(install.DEFAULT_POLICY, {'actions': {
            'screenshot': {'timeout_s': 15}, 'ax.tree': {'max_depth': 8, 'max_nodes': 2000}}})
        self.assertEqual(install.DEFAULT_POLICY['actions'],
                         {k: v for k, v in profiles['observe'].items() if not k.startswith('cua.')})
        self.assertIn('cua.status', profiles['observe'])
        self.assertEqual(set(profiles['operate_additions']), {'cua.restart', 'cua.lease', 'cua.release'})
        for removed in ['ax.press', 'input.click', 'input.keys', 'app.open']:
            self.assertNotIn(removed, {**profiles['observe'], **profiles['operate_additions']})

    def test_cli_defaults_to_root_and_user_mode_needs_no_user(self):
        from unittest.mock import patch
        with patch.object(install.sys, 'platform', 'darwin'), patch.object(install, 'Backend') as backend, \
                patch.object(install, 'cmd_status', return_value='{}') as status, patch('builtins.print'):
            backend.return_value.configure_mode.return_value = 'test-admin'
            install.main(['status', '--user', 'test-admin'])
            backend.return_value.configure_mode.assert_called_with('root', 'test-admin')
            install.main(['status', '--mode', 'user'])
            backend.return_value.configure_mode.assert_called_with('user', None)
            status.assert_called_with(backend.return_value, 'test-admin')


if __name__ == '__main__':
    unittest.main()
