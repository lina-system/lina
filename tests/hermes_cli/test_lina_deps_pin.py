"""The pins in ``pm/lina-deps.json`` (lina#32) match what the registries publish.

Network: marked ``integration`` (deselected by default; run with ``-m integration``) and skipped when the registry
cannot be reached. The OCX integrity is the registry's ``dist.integrity``, the field ``npm view <pkg>@<ver>
dist.integrity`` prints; the Node checksums are nodejs.org's ``SHASUMS256.txt`` for the pinned version.
"""
import json
import urllib.error
import urllib.parse
import urllib.request

import pytest

from hermes_cli import lina_ocx

pytestmark = pytest.mark.integration


def _get(url: str) -> bytes:
    try:
        with urllib.request.urlopen(url, timeout=20) as response:
            return response.read()
    except (urllib.error.URLError, OSError) as exc:
        pytest.skip(f"no network: {url}: {exc}")


def test_ocx_integrity_is_the_registrys():
    pin = lina_ocx.pins()
    meta = json.loads(_get(f"https://registry.npmjs.org/{urllib.parse.quote(pin.package, safe='@')}/{pin.version}"))
    assert meta["dist"]["integrity"] == pin.integrity


def test_node_checksums_are_nodejs_orgs():
    pin = lina_ocx.pins()
    sums = {name: digest for digest, name in
            (line.split() for line in _get(f"{pin.node_dist}/v{pin.node_version}/SHASUMS256.txt").decode().splitlines())}
    assert int(pin.node_version.split(".")[0]) >= 24
    for key, digest in pin.node_sha256.items():
        assert sums[f"node-v{pin.node_version}-{key}.tar.gz"] == digest
