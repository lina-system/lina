#!/usr/bin/env python3
"""Install, upgrade, roll back, inspect and remove LINA Helper on the target Mac.

Root mode (the default) on the target Mac:

    sudo /usr/bin/python3 tools/install.py install --user agent --team TEAMID0000
    sudo /usr/bin/python3 tools/install.py upgrade --user agent --app /path/LINA\\ Helper.app
    sudo /usr/bin/python3 tools/install.py rollback|status|remove --user agent

User mode (no sudo, current user):

    python3 tools/install.py install --mode user --team ABCDE12345 --app '/path/LINA Helper.app'
    python3 tools/install.py upgrade|rollback|status|remove --mode user

Every system call goes through a Backend object; tests use a fake one on a temp root.
The installer never touches TCC and never uses tccutil.
"""
import argparse
from dataclasses import dataclass
import json
import os
import plistlib
import pwd
import shutil
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from sign import BUNDLE_ID, CTL_ID, CTL_RELPATH, CUA_RELPATH, CUA_TEAM, CUA_REQUIREMENT, TEAM_RE, requirement  # noqa: E402

APP = '/Applications/LINA Helper.app'
EXE = APP + '/Contents/MacOS/lina-helper'
CTL_IN_APP = APP + '/' + CTL_RELPATH
STAGING_DIR = '/Applications/.lina-helper-staging'
STAGED = STAGING_DIR + '/LINA Helper.app'
SUPPORT = '/Library/Application Support/LINA Helper'
POLICY = SUPPORT + '/policy.json'
PREVIOUS_DIR = SUPPORT + '/previous'
PREVIOUS = PREVIOUS_DIR + '/LINA Helper.app'
AGENT_PLIST = '/Library/LaunchAgents/%s.plist' % BUNDLE_ID
CTL_LINK = '/usr/local/bin/lina-helperctl'
STATE_DIR = '/var/db/lina-helper'
STATE = STATE_DIR + '/state.json'
LABEL = BUNDLE_ID

# Everything remove may delete; a recorded path outside this set is ignored.
KNOWN_PATHS = {APP, AGENT_PLIST, POLICY, SUPPORT, PREVIOUS_DIR, PREVIOUS, CTL_LINK, STATE,
               STATE_DIR, '/usr/local/bin'}

# status and permissions are always enabled. Keep the explicit root-policy form
# for installs; the resource mirrors the profiles compiled into the signed helper.
PROFILE_RESOURCE = Path(__file__).resolve().parents[1] / 'app/Resources/PolicyProfiles.json'
PROFILES = json.loads(PROFILE_RESOURCE.read_text())
DEFAULT_POLICY = {'actions': {k: v for k, v in PROFILES['observe'].items() if not k.startswith('cua.')}}


@dataclass(frozen=True)
class Paths:
    STAGING_DIR: str = STAGING_DIR
    STAGED: str = STAGED
    SUPPORT: str = SUPPORT
    PREVIOUS_DIR: str = PREVIOUS_DIR
    PREVIOUS: str = PREVIOUS
    AGENT_PLIST: str = AGENT_PLIST
    STATE_DIR: str = STATE_DIR
    STATE: str = STATE
    CTL: str = CTL_LINK

    @classmethod
    def for_user(cls, uid, home):
        # Both sides of every bundle rename live in /Applications. Home may be on
        # another volume; only the receipt and LaunchAgent live there.
        staging = '/Applications/.lina-helper-%d-staging' % uid
        previous = '/Applications/.lina-helper-%d-previous' % uid
        support = home + '/Library/Application Support/LINA Helper/install'
        return cls(staging, staging + '/LINA Helper.app', support,
                   previous, previous + '/LINA Helper.app',
                   home + '/Library/LaunchAgents/%s.plist' % BUNDLE_ID,
                   support, support + '/state.json', CTL_IN_APP)

    def known_paths(self, mode):
        if mode == 'root':
            return KNOWN_PATHS
        return {APP, self.AGENT_PLIST, os.path.dirname(self.AGENT_PLIST),
                self.PREVIOUS_DIR, self.PREVIOUS, self.STATE, self.STATE_DIR}


class InstallError(Exception):
    pass


class Info:
    def __init__(self, identifier, adhoc, team, requirement):
        self.identifier, self.adhoc, self.team, self.requirement = identifier, adhoc, team, requirement


class Backend:
    """Real system calls. `root` re-roots every path (tests); production uses '/'."""

    def __init__(self, root='/'):
        self.root = root
        self.events = []
        self.mode = 'root'
        self.paths = Paths()

    def p(self, path):
        return os.path.join(self.root, path.lstrip('/')) if self.root != '/' else path

    def log(self, *event):
        self.events.append(event)

    # --- identity ---
    def is_root(self):
        return os.geteuid() == 0

    def current_user(self):
        entry = pwd.getpwuid(os.geteuid())
        return entry.pw_name, entry.pw_uid, entry.pw_dir

    def configure_mode(self, mode, user=None):
        if mode == 'user':
            if self.is_root():
                raise InstallError('user mode refuses root; run as the current user without sudo')
            current, uid, home = self.current_user()
            if user is not None and user != current:
                raise InstallError('--user must be the current user in user mode')
            self.paths = Paths.for_user(uid, home)
            user = current
        elif mode == 'root':
            if not user:
                raise InstallError('--user is required in root mode')
            self.paths = Paths()
        else:
            raise InstallError('unknown install mode: %s' % mode)
        self.mode = mode
        return user

    def uid_of(self, user):
        try:
            return pwd.getpwnam(user).pw_uid
        except KeyError:
            raise InstallError('no such user: %s' % user)

    # --- file system (logical paths) ---
    def exists(self, path):
        return os.path.lexists(self.p(path))

    def isdir(self, path):
        return os.path.isdir(self.p(path)) and not os.path.islink(self.p(path))

    def islink(self, path):
        return os.path.islink(self.p(path))

    def readlink(self, path):
        return os.readlink(self.p(path))

    def read_bytes(self, path):
        return Path(self.p(path)).read_bytes()

    def listdir(self, path):
        return os.listdir(self.p(path))

    def chown(self, path, owner, group):
        self.log('chown', path, owner, group)
        shutil.chown(self.p(path), owner, group)

    def makedirs(self, path, mode):
        """Create missing directories; returns the logical paths that were created."""
        created, cur = [], path
        while cur not in ('/', '') and not self.exists(cur):
            created.append(cur)
            cur = os.path.dirname(cur)
        for d in reversed(created):
            self.log('mkdir', d, mode)
            os.mkdir(self.p(d), mode)
            os.chmod(self.p(d), mode)
            if self.mode == 'root':
                self.chown(d, 'root', 'wheel')
        return list(reversed(created))

    def write_file(self, path, data, mode, owner=('root', 'wheel')):
        tmp = path + '.tmp-lina'
        self.log('write', path, mode)
        with open(self.p(tmp), 'wb') as f:
            f.write(data)
        os.chmod(self.p(tmp), mode)
        if self.mode == 'root':
            self.chown(tmp, *owner)
        os.rename(self.p(tmp), self.p(path))

    def symlink(self, target, path):
        self.log('symlink', target, path)
        os.symlink(target, self.p(path))

    def unlink(self, path):
        self.log('unlink', path)
        os.unlink(self.p(path))

    def rmdir(self, path):
        self.log('rmdir', path)
        os.rmdir(self.p(path))

    def rmtree(self, path):
        self.log('rmtree', path)
        shutil.rmtree(self.p(path))

    def rename(self, src, dst):
        self.log('rename', src, dst)
        os.rename(self.p(src), self.p(dst))

    def copy_tree(self, src, dst):
        """Copy the candidate bundle (a real path) to a logical path."""
        self.log('copy', str(src), dst)
        if os.path.exists('/usr/bin/ditto') and self.root == '/':
            subprocess.run(['/usr/bin/ditto', str(src), dst], check=True)
        else:
            shutil.copytree(str(src), self.p(dst), symlinks=True)
        # The candidate often comes from a user's staging folder; the installed app is root's.
        if self.mode == 'root':
            self.chown_tree(dst, 'root', 'wheel')

    def chown_tree(self, path, owner, group):
        self.log('chown_tree', path, owner, group)
        subprocess.run(['/usr/sbin/chown', '-R', '%s:%s' % (owner, group), self.p(path)], check=True)

    def sleep(self, seconds):
        time.sleep(seconds)

    # --- code signing (logical paths) ---
    def codesign_verify(self, path):
        r = subprocess.run(['/usr/bin/codesign', '--verify', '--strict', '--deep', self.p(path)],
                           capture_output=True, text=True)
        if r.returncode != 0:
            raise InstallError('codesign verification failed for %s: %s' % (path, r.stderr.strip()))

    def codesign_info(self, path):
        v = subprocess.run(['/usr/bin/codesign', '-d', '--verbose=4', self.p(path)],
                           capture_output=True, text=True)
        r = subprocess.run(['/usr/bin/codesign', '-d', '-r-', self.p(path)],
                           capture_output=True, text=True)
        if v.returncode != 0 or r.returncode != 0:
            raise InstallError('cannot read signature of %s: %s' % (path, (v.stderr or r.stderr).strip()))
        fields = dict(line.split('=', 1) for line in v.stderr.splitlines() if '=' in line)
        team = fields.get('TeamIdentifier')
        # codesign prints "designated => REQ", with a leading "# " on some versions.
        req = next((l.split('designated =>', 1)[1].strip() for l in r.stdout.splitlines()
                    if 'designated =>' in l), None)
        return Info(fields.get('Identifier'), fields.get('Signature') == 'adhoc',
                    None if team in (None, 'not set') else team, req)

    def codesign_check_requirement(self, path, req):
        # codesign -R treats text without a leading '=' as a requirement file path.
        r = subprocess.run(['/usr/bin/codesign', '--verify', '--strict', '-R', '=' + req, self.p(path)],
                           capture_output=True)
        return r.returncode == 0

    # --- launchd ---
    def launchctl(self, *args):
        self.log('launchctl', *args)
        r = subprocess.run(['/bin/launchctl'] + list(args), capture_output=True, text=True)
        return r.returncode, r.stdout + r.stderr

    def run_as_user(self, user, uid, argv):
        self.log('run_as_user', user, *argv)
        command = argv if self.mode == 'user' else ['/bin/launchctl', 'asuser', str(uid), '/usr/bin/sudo', '-u', user] + argv
        r = subprocess.run(command,
                           capture_output=True, text=True, timeout=30)
        return r.returncode, r.stdout


# ---------------------------------------------------------------- helpers

def bundle_version(b, app):
    try:
        return plistlib.loads(b.read_bytes(app + '/Contents/Info.plist')).get('CFBundleShortVersionString')
    except (OSError, plistlib.InvalidFileException):
        return None


def verify_candidate(b, path, team, allow_adhoc, require_cua=True):
    """Verify a bundle at logical `path`: signature, identifiers, Developer ID requirement.

    A bundle built before cua-driver was embedded has no Contents/Helpers/cua-driver. Only
    rollback accepts that (require_cua=False); a cua-driver that is present is always verified.
    """
    try:
        plist = plistlib.loads(b.read_bytes(path + '/Contents/Info.plist'))
    except (OSError, plistlib.InvalidFileException):
        raise InstallError('not an app bundle: %s' % path)
    if plist.get('CFBundleIdentifier') != BUNDLE_ID:
        raise InstallError('Info.plist bundle ID is %r, expected %s' % (plist.get('CFBundleIdentifier'), BUNDLE_ID))
    b.codesign_verify(path)
    info = b.codesign_info(path)
    if info.identifier != BUNDLE_ID:
        raise InstallError('code signing identifier is %r, expected %s' % (info.identifier, BUNDLE_ID))
    ctl = b.codesign_info(path + '/' + CTL_RELPATH)
    if ctl.identifier != CTL_ID:
        raise InstallError('lina-helperctl identifier is %r, expected %s' % (ctl.identifier, CTL_ID))
    driver_path = path + '/' + CUA_RELPATH
    if b.exists(driver_path) or require_cua:
        if not b.exists(driver_path):
            raise InstallError('bundle has no %s' % CUA_RELPATH)
        b.codesign_verify(driver_path)
        driver = b.codesign_info(driver_path)
        if (driver.identifier != 'cua-driver' or driver.adhoc or driver.team != CUA_TEAM
                or not b.codesign_check_requirement(driver_path, CUA_REQUIREMENT)):
            raise InstallError('cua-driver must retain its upstream signature (team %s)' % CUA_TEAM)
    if info.adhoc:
        if not allow_adhoc:
            raise InstallError('bundle is ad-hoc signed; grants would not survive (use --allow-ad-hoc-for-lab for a lab run)')
        return info
    if not team or not TEAM_RE.fullmatch(team):
        raise InstallError('a 10-character team ID is required (--team)')
    if info.team != team or not b.codesign_check_requirement(path, requirement(team)):
        raise InstallError('bundle does not satisfy the Developer ID requirement for team %s' % team)
    return info


class State:
    def __init__(self, b, data=None):
        self.b = b
        self.data = data or {'version': 1, 'created': []}

    @classmethod
    def load(cls, b):
        if not b.exists(b.paths.STATE):
            return None
        return cls(b, json.loads(b.read_bytes(b.paths.STATE)))

    def record(self, path, kind):
        if not any(e['path'] == path for e in self.data['created']):
            self.data['created'].append({'path': path, 'kind': kind})
        self.save()

    def save(self):
        self.b.write_file(self.b.paths.STATE, json.dumps(self.data, indent=2).encode(), 0o600)


def make_dirs(b, state, path, mode=0o755):
    for d in b.makedirs(path, mode):
        state.data['created'].append({'path': d, 'kind': 'dir'})
    state.save()


def permissions(b, user, uid):
    """Run `lina-helperctl permissions` as the GUI user; None if unavailable."""
    if not b.exists(b.paths.CTL):
        return None
    try:
        rc, out = b.run_as_user(user, uid, [b.paths.CTL, 'permissions'])
    except (OSError, subprocess.SubprocessError):
        return None
    if rc != 0:
        return None
    try:
        return json.loads(out)
    except ValueError:
        return {'raw': out.strip()}


def grants(perms):
    """Flatten the boolean leaves of a permissions result for before/after comparison."""
    out = {}

    def walk(prefix, v):
        if isinstance(v, bool):
            out[prefix] = v
        elif isinstance(v, dict):
            for k, x in v.items():
                walk(prefix + '/' + k, x)
    walk('', perms or {})
    return out


def stage(b, app):
    if b.exists(b.paths.STAGING_DIR):
        b.rmtree(b.paths.STAGING_DIR)
    b.makedirs(b.paths.STAGING_DIR, 0o700)
    b.copy_tree(app, b.paths.STAGED)


def unstage(b):
    if b.exists(b.paths.STAGING_DIR):
        b.rmtree(b.paths.STAGING_DIR)


def bootstrap(b, uid):
    rc, out = b.launchctl('bootstrap', 'gui/%d' % uid, b.paths.AGENT_PLIST)
    if rc != 0:
        raise InstallError('launchctl bootstrap failed (is %d logged in to a GUI session?): %s' % (uid, out.strip()))


def bootout(b, uid):
    b.launchctl('bootout', 'gui/%d/%s' % (uid, LABEL))  # not loaded is fine
    # bootout returns before launchd has removed the service; bootstrapping in that window
    # fails with "Bootstrap failed: 5: Input/output error". Wait until it is gone.
    for _ in range(50):
        if not agent_loaded(b, uid):
            return
        b.sleep(0.2)
    raise InstallError('launchd still lists %s 10 s after bootout' % LABEL)


def agent_loaded(b, uid):
    return b.launchctl('print', 'gui/%d/%s' % (uid, LABEL))[0] == 0


def command_uid(b, user):
    if b.mode == 'user':
        if b.is_root():
            raise InstallError('user mode refuses root')
        current, uid, _ = b.current_user()
        if user != current:
            raise InstallError('--user must be the current user in user mode')
        return uid
    if not b.is_root():
        raise InstallError('run as root (sudo)')
    return b.uid_of(user)


# ---------------------------------------------------------------- commands

def cmd_install(b, user, app, team, allow_adhoc):
    uid = command_uid(b, user)
    if b.exists(APP) or b.exists(b.paths.STATE):
        raise InstallError('already installed; use upgrade')
    if not allow_adhoc and not (team and TEAM_RE.fullmatch(team)):
        raise InstallError('--team with a 10-character team ID is required')
    for path in ((b.paths.AGENT_PLIST, CTL_LINK) if b.mode == 'root' else (b.paths.AGENT_PLIST,)):
        if b.exists(path):
            raise InstallError('%s exists and is not ours; refusing to overwrite' % path)
    try:
        stage(b, app)
        verify_candidate(b, b.paths.STAGED, team, allow_adhoc)
        state = State(b)
        state.data.update(user=user, team=team)
        make_dirs(b, state, b.paths.STATE_DIR, 0o700)
        make_dirs(b, state, b.paths.SUPPORT)
        make_dirs(b, state, os.path.dirname(b.paths.AGENT_PLIST))
        if b.mode == 'root':
            make_dirs(b, state, os.path.dirname(CTL_LINK))
        b.rename(b.paths.STAGED, APP)
        state.record(APP, 'tree')
        if b.mode == 'root' and not b.exists(POLICY):
            b.write_file(POLICY, json.dumps(DEFAULT_POLICY, indent=2).encode() + b'\n', 0o644)
            state.record(POLICY, 'file')
        plist = {'Label': LABEL, 'ProgramArguments': [EXE, 'serve'], 'KeepAlive': True,
                 'RunAtLoad': True, 'LimitLoadToSessionType': 'Aqua', 'ProcessType': 'Interactive'}
        b.write_file(b.paths.AGENT_PLIST, plistlib.dumps(plist), 0o644)
        state.record(b.paths.AGENT_PLIST, 'file')
        if b.mode == 'root':
            b.symlink(CTL_IN_APP, CTL_LINK)
            state.record(CTL_LINK, 'symlink')
        state.data['version'] = 1
        state.data['installed_version'] = bundle_version(b, APP)
        state.save()
    finally:
        unstage(b)
    bootstrap(b, uid)
    return 'installed %s' % (bundle_version(b, APP) or '?')


def check_same_requirement(installed_info, cand_info):
    if installed_info.adhoc and cand_info.adhoc:
        return  # lab only: ad-hoc has no stable requirement, grants reset anyway
    if installed_info.requirement is None or installed_info.requirement != cand_info.requirement:
        raise InstallError('designated requirement differs from the installed bundle; refusing '
                           '(grants would be lost)\n  installed: %s\n  candidate: %s'
                           % (installed_info.requirement, cand_info.requirement))


def swap_in(b, uid, new_path, old_dest):
    """installed -> old_dest; new_path -> installed; bootstrap. The caller has booted the agent out.
    Restores on failure."""
    b.rename(APP, old_dest)
    try:
        b.rename(new_path, APP)
    except OSError:
        b.rename(old_dest, APP)
        bootstrap(b, uid)
        raise InstallError('could not move the new bundle into place; previous bundle restored')
    bootstrap(b, uid)


def baseline(b, user, uid, skip):
    """Grants before a swap. Unavailable is not 'no grants': refuse unless the check is skipped."""
    if skip:
        return None
    perms = permissions(b, user, uid)
    if perms is None:
        raise InstallError('the installed helper did not answer `lina-helperctl permissions`, so grants '
                           'cannot be compared after the swap; start it or pass --skip-grant-check')
    return grants(perms)


def cmd_upgrade(b, user, app, allow_adhoc, skip_grant_check=False):
    uid = command_uid(b, user)
    state = State.load(b)
    if state is None or not b.exists(APP):
        raise InstallError('not installed; use install')
    installed = b.codesign_info(APP)
    try:
        stage(b, app)
        cand = verify_candidate(b, b.paths.STAGED, installed.team, allow_adhoc)
        check_same_requirement(installed, cand)
        before = baseline(b, user, uid, skip_grant_check)
        # Stop the agent before deleting the old backup, so a bootout timeout keeps it.
        bootout(b, uid)
        try:
            make_dirs(b, state, b.paths.SUPPORT)
            if b.exists(b.paths.PREVIOUS_DIR):
                b.rmtree(b.paths.PREVIOUS_DIR)
            b.makedirs(b.paths.PREVIOUS_DIR, 0o755)
            state.record(b.paths.PREVIOUS_DIR, 'tree')
        except BaseException:
            bootstrap(b, uid)  # put the untouched installed agent back
            raise
        swap_in(b, uid, b.paths.STAGED, b.paths.PREVIOUS)
    finally:
        unstage(b)
    state.data['installed_version'] = bundle_version(b, APP)
    state.save()
    return finish_check(b, user, uid, before, 'upgraded to %s' % state.data['installed_version'])


def finish_check(b, user, uid, before, done):
    if before is None:
        return done + ' (grant check skipped)'
    after = None
    for _ in range(5):
        after = permissions(b, user, uid)
        if after is not None:
            break
        b.sleep(1)
    if after is None:
        raise InstallError('%s, but the new helper did not answer `lina-helperctl permissions`; the '
                           'previous bundle is kept, run rollback' % done)
    if grants(after) != before:
        raise InstallError('%s, but permissions differ from before (%s -> %s); the previous bundle is '
                           'kept, run rollback' % (done, before, grants(after)))
    return done


def cmd_rollback(b, user, skip_grant_check=False):
    uid = command_uid(b, user)
    state = State.load(b)
    if state is None or not b.exists(APP):
        raise InstallError('not installed')
    if not b.exists(b.paths.PREVIOUS):
        raise InstallError('no previous bundle to roll back to')
    installed = b.codesign_info(APP)
    prev = verify_candidate(b, b.paths.PREVIOUS, installed.team, installed.adhoc, require_cua=False)
    check_same_requirement(installed, prev)
    before = baseline(b, user, uid, skip_grant_check)
    # Stop the agent before touching any bundle, so a bootout timeout leaves both in place.
    bootout(b, uid)
    try:
        unstage(b)
        b.makedirs(b.paths.STAGING_DIR, 0o700)
        b.rename(b.paths.PREVIOUS, b.paths.STAGED)  # same volume as /Applications
    except BaseException:
        bootstrap(b, uid)  # nothing was swapped; put the installed agent back
        raise
    try:
        b.rename(APP, b.paths.PREVIOUS)
    except BaseException:
        b.rename(b.paths.STAGED, b.paths.PREVIOUS)
        bootstrap(b, uid)
        raise
    try:
        b.rename(b.paths.STAGED, APP)
    except OSError:
        b.rename(b.paths.PREVIOUS, APP)
        b.rename(b.paths.STAGED, b.paths.PREVIOUS)
        bootstrap(b, uid)
        raise InstallError('could not move the previous bundle into place; nothing changed')
    bootstrap(b, uid)
    unstage(b)
    state.data['installed_version'] = bundle_version(b, APP)
    state.save()
    return finish_check(b, user, uid, before, 'rolled back to %s' % state.data['installed_version'])


def cmd_status(b, user):
    uid = command_uid(b, user)
    out = {'installed': b.exists(APP), 'user': user, 'version': None, 'requirement': None,
           'agent_loaded': agent_loaded(b, uid), 'permissions': None}
    if out['installed']:
        out['version'] = bundle_version(b, APP)
        try:
            out['requirement'] = b.codesign_info(APP).requirement
        except InstallError as e:
            out['requirement_error'] = str(e)
        out['permissions'] = permissions(b, user, uid)
    out['previous'] = b.exists(b.paths.PREVIOUS)
    return json.dumps(out, indent=2)


def cmd_remove(b, user):
    uid = command_uid(b, user)
    state = State.load(b)
    if state is None:
        raise InstallError('no state.json; nothing recorded, nothing removed')
    bootout(b, uid)
    removed = []
    entries = [e for e in state.data['created'] if e['path'] in b.paths.known_paths(b.mode) and e['path'] != b.paths.STATE]
    # Deepest paths first so directories are emptied before they are tried.
    for e in sorted(entries, key=lambda e: -e['path'].count('/')):
        path, kind = e['path'], e['kind']
        if not b.exists(path):
            continue
        if kind in ('file', 'symlink') and (kind == 'symlink') == b.islink(path):
            b.unlink(path)
        elif kind == 'tree' and b.isdir(path):
            b.rmtree(path)
        elif kind == 'dir' and b.isdir(path) and not b.listdir(path):
            b.rmdir(path)
        else:
            continue
        removed.append(path)
    b.unlink(b.paths.STATE)
    if b.paths.STATE_DIR in [e['path'] for e in entries] and b.isdir(b.paths.STATE_DIR) and not b.listdir(b.paths.STATE_DIR):
        b.rmdir(b.paths.STATE_DIR)
        removed.append(b.paths.STATE_DIR)
    return 'removed: ' + ', '.join(removed) if removed else 'nothing to remove'


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('command', choices=['install', 'upgrade', 'rollback', 'status', 'remove'])
    p.add_argument('--mode', choices=['root', 'user'], default='root')
    p.add_argument('--user', help='GUI user that runs the agent (e.g. agent)')
    p.add_argument('--app', type=Path, default=Path('build/LINA Helper.app'))
    p.add_argument('--team', help='Developer ID team (install only; upgrade uses the installed one)')
    p.add_argument('--allow-ad-hoc-for-lab', action='store_true')
    p.add_argument('--skip-grant-check', action='store_true',
                   help='upgrade/rollback: do not compare grants (helper not running)')
    a = p.parse_args(argv)
    if sys.platform != 'darwin':
        p.error('runs on macOS only')
    b = Backend()
    try:
        a.user = b.configure_mode(a.mode, a.user)
        if a.command == 'install':
            msg = cmd_install(b, a.user, a.app.resolve(), a.team, a.allow_ad_hoc_for_lab)
        elif a.command == 'upgrade':
            msg = cmd_upgrade(b, a.user, a.app.resolve(), a.allow_ad_hoc_for_lab, a.skip_grant_check)
        elif a.command == 'rollback':
            msg = cmd_rollback(b, a.user, a.skip_grant_check)
        elif a.command == 'status':
            msg = cmd_status(b, a.user)
        else:
            msg = cmd_remove(b, a.user)
    except InstallError as e:
        sys.exit('error: %s' % e)
    print(msg)


if __name__ == '__main__':
    main()
