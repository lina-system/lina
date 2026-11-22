"""The Desk publication boundary, exercised with local synthetic files only."""

import hashlib
import io
import json
import os
import tempfile
import unittest
from contextlib import redirect_stderr
from html.parser import HTMLParser
from pathlib import Path
from unittest.mock import MagicMock
from urllib.parse import unquote

from hermes_cli import desk_report_files as files
from hermes_cli.lina_reports_page import render
from hermes_state_reports import Report


def digest(data):
    return hashlib.sha256(data).hexdigest()[:16]


def references(names):
    return "\n".join(f"![figure]({name})" for name in names)


class Tags(HTMLParser):
    def __init__(self, page):
        super().__init__()
        self.images = []
        self.links = []
        self.feed(page)

    def handle_starttag(self, tag, attrs):
        if tag == "img":
            self.images.append(dict(attrs).get("src"))
        if tag == "a":
            self.links.append(dict(attrs).get("href"))


class DeskReportImages(unittest.TestCase):
    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.folder = self.root / "2_Areas" / "reports"
        self.folder.mkdir(parents=True)
        self.doc = self.folder / "sample.md"
        self.rel = self.doc.relative_to(self.root).as_posix()
        self.copies = self.root / "snapshot" / "sample.files"
        self.copies.mkdir(parents=True)
        self.row = {"slug": "sample", "rel": self.rel, "path": str(self.doc), "images": []}

    def copy_image(self, raw, data=b"image"):
        key = digest(unquote(raw).encode())
        self.row["images"].append({
            "raw": raw, "key": key, "sha": digest(data),
            "path": str(self.folder / unquote(raw)), "kind": Path(raw).suffix[1:],
        })
        copy = self.copies / key
        copy.write_bytes(data)
        return copy

    def imported(self, body, deny=()):
        from hermes_cli.desk_report_images import registry_images

        return registry_images(self.copies.parent, self.row, body, deny, desk_root=self.root)

    def test_live_order_four_rejections_and_forty_approved(self):
        names = [f"plot-{n}.png" for n in reversed(range(41))]
        for name in names:
            (self.folder / name).write_bytes(name.encode())
        (self.folder / names[0]).write_bytes(bytes(5 * 1024**2))
        (self.folder / "large.gif").write_bytes(bytes(5 * 1024**2 + 1))
        (self.folder / "unused.webp").write_bytes(b"unused")
        (self.folder / "link.jpg").symlink_to(self.root / "outside.jpg")
        (self.root / "outside.jpg").write_bytes(b"outside")
        self.doc.write_text(references(["large.gif", "text.txt", "link.jpg", *names]))
        source = files.read_source(self.root, self.rel, [])
        self.assertEqual([i.src for i in source.images], names[:40])
        self.assertEqual(len(source.images[0].data), 5 * 1024**2)
        self.assertEqual(source.images[1].data, names[1].encode())
        self.assertEqual(len(source.diagnostics), 4)

    def test_snapshot_order_four_rejections_and_copy_integrity(self):
        unused = self.copy_image("unused.png")
        self.copy_image("plot%20one.svg", b"<svg/>")
        self.copy_image("large.jpg", bytes(5 * 1024**2 + 1))
        self.copy_image("table.txt")
        self.copy_image("../outside.gif")
        chart = self.copy_image("chart.webp", b"chart")
        unused.unlink()  # No need to read an unreferenced registry entry.
        body = references(["chart.webp", "table.txt", "../outside.gif", "large.jpg", "plot%20one.svg"])
        images, diagnostics = self.imported(body)
        self.assertEqual([(i.src, i.mime, i.data) for i in images], [
            ("chart.webp", "image/webp", b"chart"), ("plot%20one.svg", "image/svg+xml", b"<svg/>")])
        self.assertEqual(len(diagnostics), 4)
        with self.assertRaises(AttributeError):
            images[0].data = b"changed"
        chart.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            self.imported(body)
        chart.unlink()
        with self.assertRaises(OSError):
            self.imported(body)
        chart.symlink_to(self.doc)
        with self.assertRaises((ValueError, OSError)):
            self.imported(body)

    def test_snapshot_uses_first_forty_references_across_all_allowed_extensions(self):
        extensions = ("png", "jpg", "jpeg", "gif", "webp", "svg")
        names = [f"image-{i}.{extensions[i % 6]}" for i in range(41)]
        for name in reversed(names):
            self.copy_image(name)
        images, diagnostics = self.imported(references(names))
        self.assertEqual([i.src for i in images], names[:40])
        self.assertEqual(len(diagnostics), 1)

    def test_snapshot_rejects_missing_and_non_string_path_metadata(self):
        self.copy_image("unused.png")
        self.copy_image("chart.png")
        body = references(["chart.png"])
        for metadata, reason in ((self.row, "registry document path"),
                                 (self.row["images"][1], "registry image 2 path")):
            original = metadata.pop("path")
            for present in (False, True):
                with self.subTest(reason=reason, present=present):
                    if present:
                        metadata["path"] = 123
                    with self.assertRaisesRegex(ValueError, reason):
                        self.imported(body)
            metadata["path"] = original

    def test_import_reports_all_bad_rows_by_ordinal_or_slug(self):
        from hermes_cli.desk_report_registry import import_registry

        self.copy_image("chart.png")
        body = references(["chart.png"]).encode()
        registry = self.copies.parent
        self.row.update(gone=False, title="Example", sha=digest(body))
        bad_doc = {key: val for key, val in self.row.items() if key != "path"}
        bad_image = dict(self.row, slug="second", images=[dict(self.row["images"][0], path=123)])
        for row in (bad_doc, bad_image):
            (registry / f"{row['slug']}.md").write_bytes(body)
        (registry / "index.json").write_text(json.dumps({"reports": [{}, bad_doc, bad_image]}))
        conn = MagicMock()
        conn.execute.return_value.fetchone.return_value = None
        errors = io.StringIO()
        with redirect_stderr(errors), self.assertRaisesRegex(files.Refused, "3 registry entries failed"):
            import_registry(conn, registry, self.root, [])
        self.assertEqual(errors.getvalue().splitlines(), [
            "entry 1: invalid slug",
            "sample: registry document path must be a nonempty string",
            "second: registry image 1 path must be a nonempty string",
        ])

    def test_markdown_size_is_measured_in_bytes_before_stripping(self):
        self.doc.write_bytes(b"# Title" + b" " * (2 * 1024**2 - 7))
        self.assertEqual(files.read_source(self.root, self.rel, []).title, "Title")
        with self.doc.open("ab") as stream:
            stream.write(b"\n")
        with self.assertRaises(files.Refused):
            files.read_source(self.root, self.rel, [])

    def test_snapshot_and_live_apply_deny_markers_and_links(self):
        names = ["ok.png", "blocked/a.jpg", "marked/a.gif", "alias/a.webp", "private/a.png",
                 "%2e%2e/other.png", "hard.png"]
        for name in names:
            self.copy_image(name)
        for part in ("blocked", "marked", "private"):
            (self.folder / part).mkdir()
        (self.folder / "marked" / ".lina-personal").touch()
        (self.folder / "alias").symlink_to(self.root, target_is_directory=True)
        (self.folder / "ok.png").write_bytes(b"image")
        outside = self.root / "outside.png"
        outside.touch()
        os.link(outside, self.folder / "hard.png")
        body = references(names)
        self.doc.write_text(body)
        deny = ["*/blocked"]
        imported, diagnostics = self.imported(body, deny)
        live = files.read_source(self.root, self.rel, deny)
        self.assertEqual(live.images, imported)
        self.assertEqual([i.src for i in imported], ["ok.png"])
        self.assertEqual(len(diagnostics), len(names) - 1)

    def test_snapshot_markdown_checks_existing_ancestors_before_missing_file(self):
        for folder in ("사람", "인물", "people", "person", "카톡", "카카오", "kakao", "katalk",
                       "메일", "mail", "murmur", "private", "개인"):
            with self.subTest(folder=folder), self.assertRaises(files.Refused):
                files.check_path(f"1_Goals/{folder}/note.md", [])
        self.assertIsNone(files.current_identity(self.root, self.rel, []))
        (self.folder / ".lina-personal").touch()
        with self.assertRaises(files.Refused):
            files.current_identity(self.root, self.rel, [])

    def test_only_inline_markdown_images_are_approved(self):
        (self.folder / "one.png").touch()
        self.doc.write_text("`![code](one.png)`\n\n```\n![code](one.png)\n```\n\n"
                            "![ref][key]\n\n[key]: one.png\n\n<img src=\"one.png\">")
        self.assertEqual(files.read_source(self.root, self.rel, []).images, ())

    def test_current_identity_does_not_consume_body_or_images(self):
        self.assertIsNone(files.current_identity(self.root, self.rel, []))
        self.doc.write_bytes(b"\xff")
        self.assertEqual(files.current_identity(self.root, self.rel, []), files.identity(self.doc.stat()))
        with self.assertRaises(files.Refused):
            files.current_identity(self.root, self.rel, ["*/sample.md"])

    def test_legacy_named_fence_does_not_swallow_following_image(self):
        from hermes_cli.lina_reports_page import body_html, image_sources

        body = ("```lina:chart\n![sample](example.png)\n```\n\n![graph](actual.png)\n\n"
                "```lina:chart\nanother chart\n```")
        self.assertEqual(image_sources(body), ("actual.png",))
        self.assertEqual(Tags(body_html(body, {"actual.png": "/asset/1"})).images, ["/asset/1"])

    def test_render_filters_images_and_keeps_other_markup(self):
        remote = "https://example.invalid/p.png"
        report = Report("r", "agent", "Doc", "Summary", None, None, False, 1,
                        references(["two.png", remote, "one.png", "bad.png"]) +
                        " after [link](/help)", 0, 0, 0)
        self.assertIn(remote, Tags(render(report)).images)
        page = render(report, {"one.png": "/assets/1", "two.png": "/assets/2",
                               "bad.png": "//example.invalid/asset"})
        self.assertEqual(Tags(page).images, ["/assets/2", "/assets/1"])
        self.assertEqual(Tags(page).links, ["/help"])
        self.assertIn(" after ", page)
        self.assertEqual(Tags(render(report, {})).images, [])

    def test_repeated_and_nested_images_cannot_exceed_visible_limit(self):
        from hermes_cli.lina_reports_page import body_html

        body = ("![one](a.png) " * 39) + "[![two](b.png)](/help) ![last](c.png)"
        urls = {f"{k}.png": f"/{k}" for k in "abc"}
        self.assertEqual(Tags(body_html(body, urls)).images, ["/a"] * 39 + ["/b"])
