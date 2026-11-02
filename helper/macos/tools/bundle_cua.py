#!/usr/bin/env python3
"""Bundle only the pinned upstream executable and notices; never re-sign them."""
import argparse
import hashlib
import json
import shutil
import subprocess
import tarfile
from pathlib import Path


def release_url(pin):
    return ('https://github.com/trycua/cua/releases/download/cua-driver-rs-v{}/{}'.format(pin['version'], pin['asset']))


def bundle(pin, archive, app):
    archive, app = Path(archive), Path(app)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != pin['sha256']:
        raise ValueError('cua-driver tarball SHA-256 mismatch')
    prefix = pin['asset'].removesuffix('.tar.gz')
    destinations = {
        'cua-driver': app / 'Contents/Helpers/cua-driver',
        'LICENSE': app / 'Contents/Resources/cua-driver/LICENSE',
        'THIRD_PARTY_NOTICES.md': app / 'Contents/Resources/cua-driver/THIRD_PARTY_NOTICES.md',
    }
    with tarfile.open(archive, 'r:gz') as source:
        # No general extractall: accept exactly three regular members, no links or traversal.
        members = {name: source.getmember(prefix + '/' + name) for name in destinations}
        if any(not member.isfile() for member in members.values()):
            raise ValueError('cua-driver archive members must be regular files')
        for name, dest in destinations.items():
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.unlink(missing_ok=True)
            with source.extractfile(members[name]) as src, dest.open('xb') as dst:
                shutil.copyfileobj(src, dst)
            dest.chmod(0o755 if name == 'cua-driver' else 0o644)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pin', type=Path, default=Path('app/cua-driver.pin'))
    parser.add_argument('--tarball', type=Path)
    parser.add_argument('--app', type=Path, required=True)
    args = parser.parse_args()
    pin = json.loads(args.pin.read_text())
    archive = args.tarball
    if archive is None:
        archive = Path('build/downloads') / pin['asset']
        archive.parent.mkdir(parents=True, exist_ok=True)
        if not archive.exists():
            partial = archive.with_suffix('.partial')
            try:
                subprocess.run(['/usr/bin/curl', '--fail', '--location', '--proto', '=https',
                                '--tlsv1.2', '--output', str(partial), release_url(pin)], check=True, timeout=600)
                if hashlib.sha256(partial.read_bytes()).hexdigest() != pin['sha256']:
                    raise ValueError('downloaded cua-driver tarball SHA-256 mismatch')
                partial.replace(archive)
            finally:
                partial.unlink(missing_ok=True)
    bundle(pin, archive, args.app)
    print('Bundled verified cua-driver {} (upstream signature preserved)'.format(pin['version']))


if __name__ == '__main__':
    main()
