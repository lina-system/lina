import hashlib
import io
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import bundle_cua


class BundleCuaTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        self.archive = self.root / 'driver.tar.gz'
        self.app = self.root / 'Test.app'
        self.pin = {'version': '0.38.0', 'asset': 'cua-driver-rs-0.38.0-darwin-arm64.tar.gz'}

    def archive_with(self, link=False):
        with tarfile.open(self.archive, 'w:gz') as archive:
            for name in ('cua-driver', 'LICENSE', 'THIRD_PARTY_NOTICES.md'):
                entry = tarfile.TarInfo('cua-driver-rs-0.38.0-darwin-arm64/' + name)
                data = (name + ' upstream bytes').encode()
                entry.size = len(data)
                if link and name == 'cua-driver':
                    entry.type = tarfile.SYMTYPE
                    entry.linkname = '/outside'
                    entry.size = 0
                archive.addfile(entry, io.BytesIO(data) if entry.isfile() else None)
        self.pin['sha256'] = hashlib.sha256(self.archive.read_bytes()).hexdigest()

    def test_copies_only_three_verified_files_without_changing_binary(self):
        self.archive_with()
        bundle_cua.bundle(self.pin, self.archive, self.app)
        exe = self.app / 'Contents/Helpers/cua-driver'
        self.assertEqual(exe.read_bytes(), b'cua-driver upstream bytes')
        self.assertEqual(exe.stat().st_mode & 0o777, 0o755)
        for name in ('LICENSE', 'THIRD_PARTY_NOTICES.md'):
            self.assertEqual((self.app / 'Contents/Resources/cua-driver' / name).read_bytes(),
                             (name + ' upstream bytes').encode())
        self.assertEqual(len([p for p in self.app.rglob('*') if p.is_file()]), 3)

    def test_checksum_failure_copies_nothing(self):
        self.archive_with()
        self.pin['sha256'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'SHA-256'):
            bundle_cua.bundle(self.pin, self.archive, self.app)
        self.assertFalse(self.app.exists())

    def test_archive_symlink_is_rejected(self):
        self.archive_with(link=True)
        with self.assertRaisesRegex(ValueError, 'regular'):
            bundle_cua.bundle(self.pin, self.archive, self.app)
        self.assertFalse(self.app.exists())

    def test_download_url_is_the_pinned_github_release(self):
        self.assertEqual(bundle_cua.release_url(self.pin),
                         'https://github.com/trycua/cua/releases/download/cua-driver-rs-v0.38.0/'
                         'cua-driver-rs-0.38.0-darwin-arm64.tar.gz')
