"""A profile's stored pictures, ``<profile>/assets/<asset>.<ext>``: the one set of rules the ws ``profiles.*_asset``
handlers (``tui_gateway/methods_profiles.py``) and the app's avatar route (``hermes_cli/lina_avatar.py``) share."""

from __future__ import annotations

from pathlib import Path

# ext -> mime; iteration order is the on-disk lookup order for assets.
ASSET_EXTS = {"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp"}
# ext -> [(start, end, magic bytes)]; format is sniffed, the declared mime is never trusted.
ASSET_MAGIC = {"png": [(0, 8, b"\x89PNG\r\n\x1a\n")], "jpg": [(0, 3, b"\xff\xd8\xff")],
               "webp": [(0, 4, b"RIFF"), (8, 12, b"WEBP")]}


def sniff(blob: bytes) -> str | None:
    """The asset ext *blob*'s bytes are, else None."""
    return next((e for e, magic in ASSET_MAGIC.items() if all(blob[a:b] == m for a, b, m in magic)), None)


def asset_file(profile_dir: Path, asset: str) -> Path | None:
    """The first ``assets/<asset>.<ext>`` that is a file, in lookup order."""
    return next((t for t in (profile_dir / "assets" / f"{asset}.{e}" for e in ASSET_EXTS) if t.is_file()), None)
