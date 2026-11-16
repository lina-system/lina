"""The report page (lina#263): one report as a plain HTML page, the "live web page" a chat link opens.

The body is the author agent's markdown, so it is untrusted: raw HTML in it is shown as text (Python-Markdown's
HTML block and inline patterns are removed), a link or image whose address is not http(s), mailto or a path of this
site is dropped, and the response's Content-Security-Policy allows no script at all. The page reloads itself while
open, so it shows the newest version.
"""

from __future__ import annotations

import html
import time
from urllib.parse import urlparse
from xml.etree.ElementTree import Element

from hermes_state_reports import Report

REFRESH_SECONDS = 30
SAFE_SCHEMES = {"http", "https", "mailto", ""}
HEADERS = {
    "Content-Security-Policy": "default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; "
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
    parsed = urlparse(url.strip())
    return parsed.scheme.lower() in SAFE_SCHEMES and (parsed.scheme or not parsed.netloc)


def _drop_unsafe_addresses(root: Element) -> None:
    for element in root.iter():
        for attribute in ("href", "src"):
            value = element.get(attribute)
            if value is not None and not _safe(value):
                del element.attrib[attribute]


def body_html(markdown_text: str) -> str:
    import markdown
    from markdown.treeprocessors import Treeprocessor

    class _Addresses(Treeprocessor):
        def run(self, root):
            _drop_unsafe_addresses(root)

    md = markdown.Markdown(extensions=["tables", "fenced_code", "sane_lists"])
    md.preprocessors.deregister("html_block")
    md.inlinePatterns.deregister("html")
    md.treeprocessors.register(_Addresses(md), "addresses", 0)
    return md.convert(markdown_text)


def render(report: Report) -> str:
    updated = time.strftime("%Y-%m-%d %H:%M UTC", time.gmtime(report.updated_at))
    meta = f"{html.escape(report.agent)} · version {report.version} · {updated}"
    return (f"<!doctype html><html><head><meta charset=\"utf-8\">"
            f"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">"
            f"<meta http-equiv=\"refresh\" content=\"{REFRESH_SECONDS}\">"
            f"<title>{html.escape(report.title)}</title><style>{_STYLE}</style></head><body>"
            f"<header><h1>{html.escape(report.title)}</h1><p>{html.escape(report.summary)}</p><p>{meta}</p></header>"
            f"<main>{body_html(report.body)}</main></body></html>")
