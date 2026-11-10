"""The pins in ``pm/lina-deps.json`` (lina#32, lina#34, lina#222) match what the registries publish.

Network: marked ``integration`` (deselected by default; run with ``-m integration``) and skipped when the registry
cannot be reached. The OCX and omo integrity is the registry's ``dist.integrity``, the field ``npm view <pkg>@<ver>
dist.integrity`` prints; the Node checksums are nodejs.org's ``SHASUMS256.txt`` for the pinned version.
"""
import json
import urllib.error
import urllib.parse
import urllib.request

import pytest

from hermes_cli import lina_ocx, lina_omo, lina_runners

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


def test_omo_integrity_is_the_registrys():
    pin = lina_omo.omo_pin()
    meta = json.loads(_get(f"https://registry.npmjs.org/{pin.package}/{pin.version}"))
    assert meta["dist"]["integrity"] == pin.integrity
    assert int(meta["engines"]["node"].lstrip(">=~^ ").split(".")[0]) <= int(lina_ocx.pins().node_version.split(".")[0])


def test_npm_runner_integrity_is_the_registrys():
    for runner in (pin for pin in lina_runners.pins().values() if pin.installer == "npm"):
        url = f"https://registry.npmjs.org/{urllib.parse.quote(runner.package, safe='@')}/{runner.version}"
        assert json.loads(_get(url))["dist"]["integrity"] == runner.integrity, runner.name


def test_runner_artifacts_are_published_where_their_installer_fetches_them():
    for runner in (pin for pin in lina_runners.pins().values() if pin.installer != "npm"):
        for key, artifact in runner.artifacts.items():
            request = urllib.request.Request(artifact["url"], method="HEAD",
                                             headers={"User-Agent": lina_runners.USER_AGENT})
            try:
                with urllib.request.urlopen(request, timeout=20) as response:
                    assert response.status == 200, (runner.name, key)
            except urllib.error.HTTPError as exc:
                pytest.fail(f"{runner.name} {key}: {artifact['url']} answered {exc.code}")
            except (urllib.error.URLError, OSError) as exc:
                pytest.skip(f"no network: {artifact['url']}: {exc}")


def test_followed_runner_sources_name_a_version_today():
    """Each ``follow`` source of the daily runner sync (lina#238) answers with a version it would move to."""
    from hermes_cli import lina_runner_sync_sources as sources
    from hermes_cli import lina_runner_sync_state as state
    for runner in (pin for pin in lina_runners.pins(moved=False).values() if pin.follow):
        try:
            version = sources.newest(runner.follow, runner.package, sources.UrlHttp(timeout=20))
        except sources.SourceError as exc:
            pytest.skip(f"no network: {runner.name}: {exc}")
        assert state.parse(version) is not None, (runner.name, version)


def test_node_checksums_are_nodejs_orgs():
    pin = lina_ocx.pins()
    sums = {name: digest for digest, name in
            (line.split() for line in _get(f"{pin.node_dist}/v{pin.node_version}/SHASUMS256.txt").decode().splitlines())}
    assert int(pin.node_version.split(".")[0]) >= 24
    for key, digest in pin.node_sha256.items():
        assert sums[f"node-v{pin.node_version}-{key}.tar.gz"] == digest
