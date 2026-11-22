"""The report page (lina#263): one report as a plain HTML page, the "live web page" a chat link opens.

The body is the author agent's markdown, so it is untrusted: raw HTML in it is shown as text (Python-Markdown's
HTML block and inline patterns are removed), a link or image whose address is not http(s), mailto or a path of this
site is dropped, and the response's Content-Security-Policy allows no script at all. The page reloads itself while
open, so it shows the newest version.
"""

from __future__ import annotations

import html
import re
import time
from collections.abc import Mapping
from urllib.parse import urlparse
from xml.etree.ElementTree import Element

from hermes_state_reports import Report

REFRESH_SECONDS = 30
IMAGE_COUNT = 40
SAFE_SCHEMES = {"http", "https", "mailto", ""}
HEADERS = {
    "Content-Security-Policy": "default-src 'none'; img-src 'self' https: data:; style-src 'unsafe-inline'; "
                               "base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
    "X-Content-Type-Options": "nosniff",
    "Referrer-Policy": "no-referrer",
    "Cache-Control": "no-store",
}
_STYLE = ("body{font:16px/1.55 -apple-system,system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;"
          "color:#1d1d1f}header p{color:#6e6e73;margin:.2rem 0}table{border-collapse:collapse}"
          "td,th{border:1px solid #d2d2d7;padding:.3rem .6rem}pre{overflow-x:auto;background:#f5f5f7;padding:.8rem}"
          "img{max-width:100%}@media(prefers-color-scheme:dark){body{background:#1d1d1f;color:#f5f5f7}"
          "pre{background:#2c2c2e}header p{color:#a1a1a6}}")


def _safe(url: str) -> bool:
    try:
        parsed = urlparse(url.strip())
    except ValueError:
        return False
    return parsed.scheme.lower() in SAFE_SCHEMES and (parsed.scheme or not parsed.netloc)


def _drop_unsafe_addresses(root: Element) -> None:
    for element in root.iter():
        for attribute in ("href", "src"):
            value = element.get(attribute)
            if value is not None and not _safe(value):
                del element.attrib[attribute]


def _markdown(*, inline_images_only: bool = False):
    import markdown
    from markdown.preprocessors import Preprocessor

    md = markdown.Markdown(extensions=["tables", "fenced_code", "sane_lists"])
    md.preprocessors.deregister("html_block")
    md.inlinePatterns.deregister("html")
    if inline_images_only:
        md.inlinePatterns.deregister("image_reference")
        md.inlinePatterns.deregister("short_image_ref")

        class _DeskFences(Preprocessor):
            def run(self, lines):
                # Python-Markdown accepts a colon in a fenced class attribute,
                # but not in a bare language name such as the legacy lina:chart.
                fence = ""
                for index, line in enumerate(lines):
                    if fence:
                        if line.rstrip() == fence:
                            fence = ""
                    elif match := re.fullmatch(r"(`{3,}|~{3,})(.*)", line):
                        fence, info = match.groups()
                        if re.fullmatch(r"lina:[\w-]+", info.strip()):
                            lines[index] = f"{fence}{{.{info.strip()}}}"
                return lines

        md.preprocessors.register(_DeskFences(md), "desk_fences", 26)
    return md


def image_sources(markdown_text: str) -> tuple[str, ...]:
    """Inline image destinations in document order, using the page's Markdown grammar."""
    from markdown.treeprocessors import Treeprocessor

    sources: list[str] = []

    class _Sources(Treeprocessor):
        def run(self, root):
            for element in root.iter("img"):
                sources.append(element.get("src", ""))

    md = _markdown(inline_images_only=True)
    md.treeprocessors.register(_Sources(md), "sources", -1)
    md.convert(markdown_text)
    return tuple(sources)


def _asset_path(url: str) -> bool:
    return (url.startswith("/") and not url.startswith("//") and "\\" not in url
            and all(ord(char) >= 32 and ord(char) != 127 for char in url))


def body_html(markdown_text: str, image_urls: Mapping[str, str] | None = None) -> str:
    from markdown.treeprocessors import Treeprocessor

    class _Addresses(Treeprocessor):
        def run(self, root):
            if image_urls is not None:
                visible = 0
                for image in root.iter("img"):
                    url = image_urls.get(image.get("src", ""))
                    if url and _asset_path(url) and visible < IMAGE_COUNT:
                        image.set("src", url)
                        visible += 1
                    else:
                        # Keep the tail, which may contain the rest of this paragraph.
                        image.tag = "span"
                        image.text = image.get("alt", "")
                        image.attrib.clear()
            _drop_unsafe_addresses(root)

    md = _markdown(inline_images_only=image_urls is not None)
    md.treeprocessors.register(_Addresses(md), "addresses", -1)
    return md.convert(markdown_text)


def render(report: Report, image_urls: Mapping[str, str] | None = None) -> str:
    updated = time.strftime("%Y-%m-%d %H:%M UTC", time.gmtime(report.updated_at))
    meta = f"{html.escape(report.agent)} · version {report.version} · {updated}"
    return (f"<!doctype html><html><head><meta charset=\"utf-8\">"
            f"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">"
            f"<meta http-equiv=\"refresh\" content=\"{REFRESH_SECONDS}\">"
            f"<title>{html.escape(report.title)}</title><style>{_STYLE}</style></head><body>"
            f"<header><h1>{html.escape(report.title)}</h1><p>{html.escape(report.summary)}</p><p>{meta}</p></header>"
            f"<main>{body_html(report.body, image_urls)}</main></body></html>")
