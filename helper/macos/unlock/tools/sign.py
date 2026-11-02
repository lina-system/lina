#!/usr/bin/env python3
"""Sign the built package with an explicitly selected Developer ID identity.

The GUI agent claims `com.apple.application-identifier` and
`keychain-access-groups`. On macOS both are restricted entitlements: AMFI kills
the process at launch unless an embedded provisioning profile authorizes them
(TN3125, TN3137). Signing therefore requires a Developer ID provisioning
profile for the agent's App ID and refuses to produce an unlaunchable agent.
"""
import argparse
import datetime
import hashlib
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import sys
import tempfile

BUNDLE_ID = 'local.hermes.unlock.agent'


def requirement(team):
    return ('anchor apple generic and certificate leaf[subject.OU] = "'+team+'" and '
            'certificate 1[field.1.2.840.113635.100.6.2.6] exists and '
            'certificate leaf[field.1.2.840.113635.100.6.1.13] exists')


def allowed(pattern, value):
    return pattern == value or (pattern.endswith('.*') and value.startswith(pattern[:-1]))


def check_profile(profile, team, claimed, leaf_sha256=None, now=None):
    """Return the reasons a decoded profile cannot authorize the `claimed` entitlements."""
    problems = []
    ents = profile.get('Entitlements', {})
    if team not in profile.get('TeamIdentifier', []): problems.append('profile team mismatch')
    if profile.get('ProvisionsAllDevices') is not True:
        problems.append('not a Developer ID profile (ProvisionsAllDevices missing)')
    expires = profile.get('ExpirationDate')
    now = now or datetime.datetime.now(datetime.UTC)
    if expires is None or expires.replace(tzinfo=datetime.UTC) <= now: problems.append('profile expired')
    if ents.get('com.apple.developer.team-identifier') != team:
        problems.append('profile team-identifier entitlement mismatch')
    if not allowed(ents.get('com.apple.application-identifier', ''), claimed['com.apple.application-identifier']):
        problems.append('profile App ID does not cover '+claimed['com.apple.application-identifier'])
    for group in claimed['keychain-access-groups']:
        if not any(allowed(g, group) for g in ents.get('keychain-access-groups', [])):
            problems.append('profile does not allow keychain group '+group)
    if leaf_sha256 is not None:
        listed = [hashlib.sha256(c).hexdigest() for c in profile.get('DeveloperCertificates', [])]
        if leaf_sha256 not in listed: problems.append('signing certificate is not listed in the profile')
    return problems


def decode_profile(path):
    out = subprocess.run(['/usr/bin/security', 'cms', '-D', '-i', str(path)],
                         check=True, capture_output=True, timeout=60).stdout
    return plistlib.loads(out)


def leaf_certificate_sha256(artifact):
    """SHA-256 of the leaf certificate actually embedded in a signed artifact."""
    with tempfile.TemporaryDirectory() as tmp:
        subprocess.run(['/usr/bin/codesign', '-d', '--extract-certificates='+tmp+'/c', str(artifact)],
                       check=True, capture_output=True, timeout=60)
        return hashlib.sha256(Path(tmp, 'c0').read_bytes()).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--identity', required=True)
    p.add_argument('--team', required=True)
    p.add_argument('--build', type=Path, default=Path(__file__).resolve().parents[1]/'build')
    p.add_argument('--provisioning-profile', type=Path, required=True,
                   help='Developer ID provisioning profile for TEAM.'+BUNDLE_ID)
    a = p.parse_args()
    if sys.platform != 'darwin' or not re.fullmatch('[A-Z0-9]{10}', a.team):
        p.error('requires macOS and a 10-character Apple team identifier')
    app = a.build/'Hermes Unlock Agent.app'
    entitlements = {'com.apple.application-identifier': a.team+'.'+BUNDLE_ID,
                    'com.apple.developer.team-identifier': a.team,
                    'keychain-access-groups': [a.team+'.'+BUNDLE_ID]}
    profile = decode_profile(a.provisioning_profile)
    problems = check_profile(profile, a.team, entitlements)
    if problems: p.error('provisioning profile rejected: '+'; '.join(problems))
    shutil.copy2(a.provisioning_profile, app/'Contents/embedded.provisionprofile')
    path = a.build/'agent.entitlements.plist'; path.write_bytes(plistlib.dumps(entitlements))
    req = requirement(a.team)
    for artifact in [a.build/'hermes-unlockctl', a.build/'HermesUnlockAuthorizationPlugin.bundle', app]:
        args = ['/usr/bin/codesign', '--force', '--sign', a.identity, '--options', 'runtime', '--timestamp']
        if artifact == app: args += ['--entitlements', str(path)]
        subprocess.run(args+[str(artifact)], check=True, timeout=300)
        # codesign -R treats text without a leading '=' as a requirement file path.
        subprocess.run(['/usr/bin/codesign', '--verify', '--strict', '-R', '='+req, str(artifact)], check=True, timeout=120)
    problems = check_profile(profile, a.team, entitlements, leaf_certificate_sha256(app))
    if problems: sys.exit('signed agent does not match its profile: '+'; '.join(problems))
    # AMFI SIGKILLs an agent whose restricted entitlements are not authorized; prove it launches.
    smoke = subprocess.run([str(app/'Contents/MacOS/hermes-unlock-agent'), 'permissions'],
                           capture_output=True, check=False, timeout=20)
    if smoke.returncode != 0:
        sys.exit(f'signed agent failed to launch (exit {smoke.returncode}); check the AMFI log for restricted '
                 'entitlements')
    print('Signed all three artifacts; the agent launches with its embedded profile. Run live acceptance on the target Mac.')


if __name__ == '__main__': main()
