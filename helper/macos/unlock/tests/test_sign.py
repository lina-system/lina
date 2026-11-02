import datetime
import hashlib
from pathlib import Path
import sys
import unittest
sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'tools'))
import sign

TEAM = 'ABCDE12345'
CLAIM = {'com.apple.application-identifier': TEAM+'.local.hermes.unlock.agent',
         'com.apple.developer.team-identifier': TEAM,
         'keychain-access-groups': [TEAM+'.local.hermes.unlock.agent']}
NOW = datetime.datetime(2026, 10, 25, tzinfo=datetime.UTC)
CERT = b'synthetic leaf certificate'


def profile(**over):
    value = {'TeamIdentifier': [TEAM], 'ProvisionsAllDevices': True,
             'ExpirationDate': datetime.datetime(2044, 1, 1),
             'DeveloperCertificates': [CERT],
             'Entitlements': {'com.apple.application-identifier': TEAM+'.local.hermes.unlock.agent',
                              'com.apple.developer.team-identifier': TEAM,
                              'keychain-access-groups': [TEAM+'.*']}}
    for key, item in over.items():
        if key in value['Entitlements']: value['Entitlements'][key] = item
        else: value[key] = item
    return value


class SignProfileTests(unittest.TestCase):
    def test_developer_id_profile_for_agent_is_accepted(self):
        leaf = hashlib.sha256(CERT).hexdigest()
        self.assertEqual(sign.check_profile(profile(), TEAM, CLAIM, leaf, NOW), [])

    def test_profiles_that_amfi_would_reject_are_refused(self):
        cases = {'not a Developer ID profile': profile(ProvisionsAllDevices=None),
                 'expired': profile(ExpirationDate=datetime.datetime(2026, 1, 1)),
                 'App ID does not cover': profile(**{'com.apple.application-identifier': TEAM+'.other.app'}),
                 'keychain group': profile(**{'keychain-access-groups': [TEAM+'.other.app']}),
                 'team mismatch': profile(TeamIdentifier=['ZZZZZ99999'])}
        for reason, value in cases.items():
            with self.subTest(reason=reason):
                self.assertTrue(any(reason in p for p in sign.check_profile(value, TEAM, CLAIM, now=NOW)))

    def test_wildcard_app_id_allows_only_its_prefix(self):
        self.assertTrue(sign.allowed(TEAM+'.*', TEAM+'.local.hermes.unlock.agent'))
        self.assertFalse(sign.allowed(TEAM+'.*', 'ZZZZZ99999.local.hermes.unlock.agent'))

    def test_signing_certificate_must_be_listed(self):
        problems = sign.check_profile(profile(), TEAM, CLAIM, hashlib.sha256(b'other').hexdigest(), NOW)
        self.assertIn('signing certificate is not listed in the profile', problems)


if __name__ == '__main__': unittest.main()
