"""Photon sidecar in the built image."""
import pytest
from tests.docker.conftest import _docker_available
from tests.docker.test_tui_prebuilt_bundle import _exec_py

pytestmark = pytest.mark.skipif(not _docker_available(), reason="Docker not available or daemon not running")
pytest_plugins = ["tests.docker.conftest"]

def test_photon_baked_dependencies_load_without_writes_or_network(built_image: str) -> None:
    """The non-root runtime uses the baked sidecar, including its npm patch."""
    py = '''
import subprocess
from plugins.platforms.photon.sidecar_paths import SOURCE_SIDECAR_DIR, resolve_sidecar_dir, dir_writable
sidecar = resolve_sidecar_dir()
assert sidecar == SOURCE_SIDECAR_DIR, sidecar
assert not dir_writable(sidecar / 'node_modules')
child = subprocess.run(['node', '--input-type=module', '-e',
    "import {patchSpectrumTs} from './patch-spectrum-mixed-attachments.mjs'; "
    "patchSpectrumTs(); await import('spectrum-ts'); console.log('PHOTON_LOADED')"],
    cwd=sidecar, capture_output=True, text=True, timeout=30)
assert child.returncode == 0, child.stderr
print(child.stdout.strip())
'''
    assert _exec_py(built_image, py).endswith('PHOTON_LOADED')
