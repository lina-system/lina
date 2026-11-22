"""Select Desk report images in Markdown order and freeze their bytes for publication."""

from __future__ import annotations

import hashlib
import re
from collections.abc import Callable, Mapping, Sequence
from contextlib import ExitStack
from dataclasses import dataclass, field
from pathlib import Path
from typing import TypedDict
from urllib.parse import unquote, urlsplit

from hermes_cli.desk_report_files import (
    MARKDOWN_LIMIT,
    Refused,
    _check_desk_path,
    _current_file_identity,
    bounded_read,
    check_path,
    open_regular,
)
from hermes_cli.lina_reports_page import IMAGE_COUNT, image_sources

IMAGE_LIMIT = 5 * 1024**2
MIME = {".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
        ".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml"}


@dataclass(frozen=True, slots=True)
class Image:
    src: str
    mime: str
    data: bytes = field(repr=False)


class RegistryRow(TypedDict):
    slug: str
    path: str
    rel: str
    images: list[dict[str, str]]


Selection = tuple[tuple[Image, ...], tuple[str, ...]]


def _relative_image(raw: str) -> Path:
    try:
        decoded = unquote(raw, errors="strict")
        url = urlsplit(decoded)
    except (UnicodeError, ValueError) as exc:
        raise Refused("invalid image address") from exc
    if (url.scheme or url.netloc or url.query or url.fragment or "\\" in decoded
            or any(ord(char) < 32 for char in decoded) or decoded.startswith("/")):
        raise Refused("only relative image paths are allowed")
    path = Path(decoded)
    if ".." in path.parts:
        raise Refused("image path leaves its folder")
    if path.suffix.lower() not in MIME:
        raise Refused("unsupported image extension")
    return path


def _select(
    sources: Sequence[str], document: Path, deny: Sequence[str], load: Callable[[str, Path], bytes],
) -> Selection:
    images: dict[str, Image] = {}
    visible = 0
    diagnostics: list[str] = []
    for index, src in enumerate(sources, start=1):
        try:
            if visible >= IMAGE_COUNT:
                raise Refused("40 image limit reached")
            if src not in images:
                path = document.parent / _relative_image(src)
                _check_desk_path(path, deny)
                images[src] = Image(src, MIME[path.suffix.lower()], load(src, path))
            visible += 1
        except Refused as exc:
            # Ordinals identify references without logging private paths or data URLs.
            diagnostics.append(f"image reference {index}: {exc}")
    return tuple(images.values()), tuple(diagnostics)


def live_images(root: Path, document: Path, body: str, deny: Sequence[str]) -> Selection:
    def load(src: str, path: Path) -> bytes:
        with open_regular(root, path, missing_ok=True) as fd:
            if fd is None:
                raise Refused("referenced image is missing")
            return bounded_read(fd, IMAGE_LIMIT)

    return _select(image_sources(body), document, deny, load)


def _digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()[:16]


def registry_images(root: Path, row: RegistryRow, body: str, deny: Sequence[str],
                    *, desk_root: Path | None = None) -> Selection:
    """Read only indexed, referenced copies. A broken approved copy aborts the import."""
    if len(body.encode("utf-8")) > MARKDOWN_LIMIT:
        raise Refused("markdown exceeds 2 MiB limit")
    document = check_path(row["rel"], deny)
    slug = row["slug"]
    if not isinstance(slug, str) or not re.fullmatch(r"[A-Za-z0-9_-]+", slug):
        raise ValueError("invalid registry slug")
    entries = row.get("images", [])
    if not isinstance(entries, list):
        raise ValueError("invalid registry image list")
    indexed: dict[str, tuple[int, Mapping[str, str]]] = {}
    for number, entry in enumerate(entries, 1):
        if not isinstance(entry, dict) or not isinstance(entry.get("raw"), str):
            raise ValueError("invalid registry image record")
        if entry["raw"] in indexed:
            raise ValueError("duplicate registry image reference")
        indexed[entry["raw"]] = number, entry

    used_keys: set[str] = set()

    def load(src: str, path: Path) -> bytes:
        record = indexed.get(src)
        if record is None:
            raise Refused("image absent from registry")
        number, item = record
        if desk_root is not None:
            _current_file_identity(desk_root, path)
        decoded = unquote(src)
        key = _digest(decoded.encode("utf-8"))
        if key != item.get("key") or key in used_keys:
            raise ValueError("invalid or ambiguous registry image key")
        used_keys.add(key)
        if MIME.get("." + str(item.get("kind")).lower()) != MIME[path.suffix.lower()]:
            raise ValueError("registry image type mismatch")
        document_path = row.get("path")
        if not isinstance(document_path, str) or not document_path:
            raise ValueError("registry document path must be a nonempty string")
        image_path = item.get("path")
        if not isinstance(image_path, str) or not image_path:
            raise ValueError(f"registry image {number} path must be a nonempty string")
        if Path(document_path).parent / decoded != Path(image_path):
            raise ValueError("registry image source path mismatch")
        copy = Path(f"{slug}.files") / key
        # Policy rejections skip a reference, but an unsafe snapshot file is
        # a broken approved copy, just like a missing or unreadable one.
        with ExitStack() as stack:
            try:
                fd = stack.enter_context(open_regular(root, copy))
            except Refused as exc:
                raise ValueError("unsafe approved registry image copy") from exc
            assert fd is not None
            data = bounded_read(fd, IMAGE_LIMIT)
        if _digest(data) != item.get("sha"):
            raise ValueError("registry image content hash mismatch")
        return data

    sources = image_sources(body)
    images, diagnostics = _select(sources, document, deny, load)
    referenced = set(sources)
    unreferenced = tuple(f"registry image {index}: not referenced in Markdown"
                         for index, src in enumerate(indexed, start=1) if src not in referenced)
    return images, diagnostics + unreferenced
