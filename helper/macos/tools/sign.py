#!/usr/bin/env python3
"""Sign LINA Helper.app with the team Developer ID (agent-hub only).

Signs Contents/Helpers/lina-helperctl (identifier app.lina-os.helper.ctl) and then the
app (identifier app.lina-os.helper), inside-out, with the hardened runtime and a secure
timestamp, and verifies both against the designated requirement of docs/design.md section 2.
v0 has no restricted entitlements, so there is no provisioning profile.
"""
import argparse
import plistlib
import re
import subprocess
import sys
from pathlib import Path

BUNDLE_ID = 'app.lina-os.helper'
CTL_ID = 'app.lina-os.helper.ctl'
CTL_RELPATH = 'Contents/Helpers/lina-helperctl'
CUA_RELPATH = 'Contents/Helpers/cua-driver'
CUA_TEAM = 'YCK386LBJ7'
CUA_REQUIREMENT = 'identifier "cua-driver" and anchor apple generic and certificate leaf[subject.OU] = "YCK386LBJ7"'
IDENTITY_RE = re.compile(r'Developer ID Application: .+ \(([A-Z0-9]{10})\)')
TEAM_RE = re.compile(r'[A-Z0-9]{10}')
DEFAULT_APP = Path(__file__).resolve().parents[1] / 'build' / 'LINA Helper.app'


class SignError(Exception):
    pass


def requirement(team, identifier=BUNDLE_ID):
    return ('identifier "%s" and anchor apple generic and '
            'certificate 1[field.1.2.840.113635.100.6.2.6] and '
            'certificate leaf[field.1.2.840.113635.100.6.1.13] and '
            'certificate leaf[subject.OU] = "%s"' % (identifier, team))


def check_inputs(identity, team, app):
    if not TEAM_RE.fullmatch(team):
        raise SignError('team must be 10 characters [A-Z0-9]: %r' % team)
    m = IDENTITY_RE.fullmatch(identity)
    if not m:
        raise SignError("identity must look like 'Developer ID Application: NAME (TEAMID)'")
    if m.group(1) != team:
        raise SignError('identity team %s differs from --team %s' % (m.group(1), team))
    plist = Path(app) / 'Contents' / 'Info.plist'
    if not plist.is_file():
        raise SignError('not an app bundle (no Contents/Info.plist): %s' % app)
    found = plistlib.loads(plist.read_bytes()).get('CFBundleIdentifier')
    if found != BUNDLE_ID:
        raise SignError('Info.plist bundle ID is %r, expected %s' % (found, BUNDLE_ID))
    if not (Path(app) / CTL_RELPATH).is_file():
        raise SignError('missing %s' % CTL_RELPATH)
    if not (Path(app) / CUA_RELPATH).is_file():
        raise SignError('missing %s' % CUA_RELPATH)


def sign(app, identity, team, run=subprocess.run):
    check_inputs(identity, team, app)
    app = Path(app)
    # Keep cua-driver's upstream YCK386LBJ7 signature. Never use --deep to sign.
    steps = [(app / CTL_RELPATH, CTL_ID), (app, BUNDLE_ID)]
    for path, ident in steps:
        run(['/usr/bin/codesign', '--force', '--sign', identity, '--identifier', ident,
             '--options', 'runtime', '--timestamp', str(path)], check=True)
        # codesign -R treats text without a leading '=' as a requirement file path.
        run(['/usr/bin/codesign', '--verify', '--strict', '-R', '=' + requirement(team, ident),
             str(path)], check=True)
    run(['/usr/bin/codesign', '--verify', '--strict', '--deep', str(app)], check=True)


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--identity', required=True, help="'Developer ID Application: NAME (TEAMID)'")
    p.add_argument('--team', required=True, help='10-character Apple team identifier')
    p.add_argument('--app', type=Path, default=DEFAULT_APP)
    a = p.parse_args(argv)
    if sys.platform != 'darwin':
        p.error('signing requires macOS')
    try:
        sign(a.app, a.identity, a.team)
    except SignError as e:
        p.error(str(e))
    except subprocess.CalledProcessError as e:
        sys.exit('codesign failed (exit %d): %s' % (e.returncode, ' '.join(map(str, e.cmd))))
    print('Signed and verified %s (team %s).' % (a.app, a.team))


if __name__ == '__main__':
    main()
