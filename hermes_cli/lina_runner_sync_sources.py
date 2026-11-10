"""Where the daily runner sync (lina#238) reads each runner's newest version, and how it checks the bytes.

A pin's ``follow`` names one source:

* ``{"npm": "<dist-tag>"}``: the registry's dist-tag; the integrity is that version's ``dist.integrity``.
* ``{"channel": <url>, "artifact": <url>, "platforms": {...}}``: the channel's body is the version (Grok).
* ``{"page": <url>, "pattern": <regex>, "artifact": <url>, "platforms": {...}}``: the version is the regex's group
  in the installer page (cursor-agent).
* ``{"manifest": <url>, "platforms": {...}}``: one JSON manifest per platform with ``version``, ``url``, ``sha512``
  (agy).

A source that publishes no hash (``channel``, ``page``) is downloaded by the hub for each platform the nodes use and
its sha256 recorded; when the download's own answer carries a hash (GCS ``x-goog-hash`` md5, a plain md5 ``etag``)
or a length, the bytes must match it, else nothing moves.
"""

from __future__ import annotations

import base64
import hashlib
import json
import re
import shutil
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Protocol

REGISTRY = "https://registry.npmjs.org"
USER_AGENT = "lina-runners"
_SAFE_VERSION = re.compile(r"^[0-9][0-9A-Za-z.\-]{0,63}$")


class SourceError(RuntimeError):
    pass


class Http(Protocol):
    def text(self, url: str) -> str: ...

    def file(self, url: str, dest: Path) -> dict[str, str]: ...


class UrlHttp:
    """The real network: small reads into memory, artifacts streamed to a file with the answer's headers."""

    def __init__(self, timeout: float = 60):
        self.timeout = timeout

    def _open(self, url: str, timeout: float):
        request = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
        try:
            return urllib.request.urlopen(request, timeout=timeout)
        except (urllib.error.URLError, OSError) as exc:
            raise SourceError(f"{url}: {exc}") from exc

    def text(self, url: str) -> str:
        with self._open(url, self.timeout) as response:
            return response.read().decode("utf-8", "replace")

    def file(self, url: str, dest: Path) -> dict[str, str]:
        try:
            with self._open(url, 600) as response, open(dest, "wb") as fh:
                shutil.copyfileobj(response, fh)
                return {key.lower(): value for key, value in response.headers.items()}
        except OSError as exc:
            raise SourceError(f"{url}: {exc}") from exc


@dataclass
class Found:
    """The newest version a source names, and what a pin of it needs (npm integrity or per-platform artifacts)."""
    version: str
    integrity: str = ""
    artifacts: dict = field(default_factory=dict)
    downloads: dict = field(default_factory=dict)
    notes: list = field(default_factory=list)


def _version(value: str, where: str) -> str:
    value = (value or "").strip()
    if not _SAFE_VERSION.match(value):
        raise SourceError(f"{where} gave no version ({value[:40]!r})")
    return value


def _json(http: Http, url: str) -> dict:
    try:
        value = json.loads(http.text(url))
    except ValueError as exc:
        raise SourceError(f"{url} is not JSON") from exc
    if not isinstance(value, dict):
        raise SourceError(f"{url} is not a JSON object")
    return value


def newest(follow: dict, package: str, http: Http) -> str:
    if "npm" in follow:
        tags = _json(http, f"{REGISTRY}/-/package/{urllib.parse.quote(package, safe='@')}/dist-tags")
        return _version(str(tags.get(follow["npm"]) or ""), f"npm dist-tag {follow['npm']} of {package}")
    if "channel" in follow:
        lines = http.text(follow["channel"]).strip().splitlines()
        return _version(lines[0] if lines else "", follow["channel"])
    if "page" in follow:
        found = re.search(follow["pattern"], http.text(follow["page"]))
        return _version(found.group(1) if found else "", follow["page"])
    if "manifest" in follow:
        versions = {_version(str(_json(http, follow["manifest"].format(platform=name)).get("version") or ""),
                             follow["manifest"].format(platform=name)) for name in follow["platforms"].values()}
        if len(versions) != 1:
            raise SourceError(f"the manifests name more than one version: {', '.join(sorted(versions))}")
        return versions.pop()
    raise SourceError(f"unknown follow source {sorted(follow)}")


def _md5(path: Path) -> str:
    digest = hashlib.md5(usedforsecurity=False)
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def check_answer(path: Path, headers: dict[str, str], url: str) -> str:
    """Compare the downloaded bytes with the hash or length the answer carried; returns what was compared."""
    compared = []
    length = headers.get("content-length")
    if length and length.isdigit():
        if int(length) != path.stat().st_size:
            raise SourceError(f"{url}: {path.stat().st_size} bytes, the answer said {length}")
        compared.append("length")
    for part in (headers.get("x-goog-hash") or "").split(","):
        name, _, value = part.strip().partition("=")
        if name == "md5":
            got = base64.b64encode(bytes.fromhex(_md5(path))).decode()
            if got != value.strip():
                raise SourceError(f"{url}: md5 {got} does not match the answer's x-goog-hash md5 {value.strip()}")
            compared.append("x-goog-hash md5")
    etag = (headers.get("etag") or "").strip().strip('"')
    if re.fullmatch(r"[0-9a-f]{32}", etag):
        if _md5(path) != etag:
            raise SourceError(f"{url}: md5 {_md5(path)} does not match the answer's etag {etag}")
        compared.append("etag md5")
    return ", ".join(compared) or "none"


def resolve(follow: dict, package: str, version: str, platforms: set[str], http: Http, workdir: Path) -> Found:
    """What a pin of *version* needs: the npm integrity, or an artifact (url and hash) for each of *platforms* (LINA
    platform keys such as ``linux-x64``) the source serves."""
    found = Found(version)
    if "npm" in follow:
        meta = _json(http, f"{REGISTRY}/{urllib.parse.quote(package, safe='@')}/{version}")
        integrity = str((meta.get("dist") or {}).get("integrity") or "")
        if not integrity.startswith("sha512-"):
            raise SourceError(f"{package}@{version} has no sha512 dist.integrity; not moved without a hash")
        found.integrity = integrity
        return found
    wanted = {key: follow["platforms"][key] for key in sorted(platforms) if key in follow["platforms"]}
    if not wanted:
        raise SourceError(f"no platform of {sorted(platforms)} is served by this source")
    for key, name in wanted.items():
        if "manifest" in follow:
            manifest = _json(http, follow["manifest"].format(platform=name))
            if manifest.get("version") != version or not re.fullmatch(r"[0-9a-f]{128}", str(manifest.get("sha512"))):
                raise SourceError(f"the {name} manifest has no sha512 for {version}")
            found.artifacts[key] = {"url": str(manifest["url"]), "sha512": manifest["sha512"]}
            continue
        url = follow["artifact"].format(version=version, platform=name)
        workdir.mkdir(parents=True, exist_ok=True)
        dest = workdir / f"{key}.download"
        headers = http.file(url, dest)
        compared = check_answer(dest, headers, url)
        found.artifacts[key] = {"url": url, "sha256": _sha256(dest)}
        found.downloads[url] = dest
        found.notes.append(f"{key}: sha256 recorded, answer compared by {compared}")
    return found
