"""An agent's picture for the app (lina#738): ``GET /api/lina/agents/{agent}/avatar`` and the ``avatar`` flag of the
people list (``lina_board_items``). The id is looked up in the record's agents (``work.agents``) and mapped to that
profile's directory, and the file is the profile's ``assets/avatar.<ext>`` (``profile_assets``): no path comes from
the request. A file that resolves outside ``assets/`` (a symlink), is empty or over 1 MB, or is not a PNG, JPEG or
WebP Pillow can open is no picture. The body is at most 128 px on its long side: a smaller PNG or JPEG as stored, else
downsized (a JPEG stays a JPEG, the rest become PNG). Pillow is a core dependency on Python 3.14+ only; without it
there is no picture, and the app keeps its letter circle."""

from __future__ import annotations

import hashlib
import io
from dataclasses import dataclass

SIDE = 128
MAX_BYTES = 1 << 20
_MAX_PIXELS = 4096 * 4096
_SERVED_AS_STORED = {"PNG": "image/png", "JPEG": "image/jpeg"}


@dataclass(frozen=True)
class Source:
    blob: bytes
    etag: str


def _stored(agent: str) -> bytes | None:
    from hermes_cli.profile_assets import asset_file, sniff
    from hermes_cli.profiles import get_profile_dir
    try:
        profile_dir = get_profile_dir(agent)
    except ValueError:
        return None
    path = asset_file(profile_dir, "avatar")
    if path is None:
        return None
    try:
        assets, real = (profile_dir / "assets").resolve(), path.resolve()
        if not (assets.is_relative_to(profile_dir.resolve()) and real.is_relative_to(assets)):
            return None
        with real.open("rb") as f:
            blob = f.read(MAX_BYTES + 1)
    except OSError:
        return None
    if not 0 < len(blob) <= MAX_BYTES or sniff(blob) is None:
        return None
    image = _opened(blob)
    if image is None:
        return None
    image.close()
    return blob


def _opened(blob: bytes):
    """The image *blob* is, its header read, when Pillow can open it and it is not oversized; else None."""
    try:
        from PIL import Image
    except ImportError:
        return None
    try:
        image = Image.open(io.BytesIO(blob))
    except (OSError, ValueError, Image.DecompressionBombError):
        return None
    if image.format not in (*_SERVED_AS_STORED, "WEBP") or image.width * image.height > _MAX_PIXELS:
        image.close()
        return None
    return image


def has_picture(agent: str) -> bool:
    """Whether *agent*, one of the record's agents, has a picture to serve."""
    return _stored(agent) is not None


def source(agent: str) -> Source | None:
    """*agent*'s stored picture and its ETag, or None when it is not one of the record's agents or has none."""
    from hermes_cli.lina_deps import lina_root
    from hermes_cli.work import env
    if agent not in env.load(lina_root()).agents:
        return None
    blob = _stored(agent)
    if blob is None:
        return None
    return Source(blob, '"' + hashlib.sha256(b"%d:" % SIDE + blob).hexdigest()[:32] + '"')


def downsized(blob: bytes) -> tuple[bytes, str] | None:
    """``(body, media type)`` of *blob* at most SIDE px on its long side, or None when it does not decode."""
    from PIL import Image
    image = _opened(blob)
    if image is None:
        return None
    with image:
        try:
            image.load()
            fmt = image.format
            if max(image.size) <= SIDE and fmt in _SERVED_AS_STORED:
                return blob, _SERVED_AS_STORED[fmt]
            if fmt == "JPEG":
                image = image.convert("RGB")
            elif image.mode not in ("RGB", "RGBA", "L", "LA"):
                image = image.convert("RGBA")
            image.thumbnail((SIDE, SIDE))
            out = io.BytesIO()
            if fmt == "JPEG":
                image.save(out, "JPEG", quality=90)
            else:
                image.save(out, "PNG", optimize=True)
        except (OSError, ValueError, Image.DecompressionBombError):
            return None
    return out.getvalue(), "image/jpeg" if fmt == "JPEG" else "image/png"
