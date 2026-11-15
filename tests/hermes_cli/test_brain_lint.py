"""lina#443: lint inspects written pages without modifying their bytes."""

from __future__ import annotations

from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
from hermes_cli.brain.scan import scan


def test_lint_reports_every_rule_with_paths_and_keeps_files_unchanged(tmp_path):
    from hermes_cli.brain.lint import lint

    root = tmp_path / "wiki"
    pages = {
        "empty.md": "Only text. [[absent]]\n",
        "concepts/shared.md": "---\ntitle: First\naliases: [same]\n---\n",
        "ideas/shared.md": "---\ntitle: Second\naliases: [same]\n---\n",
        "typed.md": "---\ntype: unregistered\n---\n# Typed\n",
        "dates.md": "# Dates\n### 2026-03-05\n### 2026-01-01\n\n---\n## Timeline\n"
                    "### 2026-03-02\n### 2026-03-01\n",
        "valid.md": "# Valid\n[[First]] [[Second]] `[[absent]]`\n"
                    "```\n[[absent]]\n```\n",
        "unknown/page.md": "# Page\n",
    }
    for rel, body in pages.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body, encoding="utf-8")
    before = {rel: (root / rel).read_bytes() for rel in pages}

    findings = lint(scan(root, mapping_from(DEFAULT_MAPPING)))
    rules = {(finding.path, finding.rule) for finding in findings}

    assert rules == {
        ("empty.md", "missing-title"), ("empty.md", "broken-link"),
        ("concepts/shared.md", "duplicate-slug"), ("ideas/shared.md", "duplicate-slug"),
        ("concepts/shared.md", "duplicate-alias"), ("ideas/shared.md", "duplicate-alias"),
        ("typed.md", "unknown-type"), ("unknown/page.md", "unknown-type"),
        ("dates.md", "timeline-order"),
    }
    assert {rel: (root / rel).read_bytes() for rel in pages} == before
    assert findings == lint(scan(root, mapping_from(DEFAULT_MAPPING)))


def test_title_code_and_dates_above_divider_do_not_fool_lint(tmp_path):
    from hermes_cli.brain.lint import lint

    root = tmp_path / "wiki"
    root.mkdir()
    (root / "untitled.md").write_text("```\n# Not a title\n```\n", encoding="utf-8")
    (root / "valid.md").write_text(
        "---\ntitle: Real title\n---\n### 2026-04-05\n### 2026-01-01\n"
        "---\n## Timeline\n### 2026-01-01\n```\n### 2025-01-01\n```\n### 2026-04-05\n",
        encoding="utf-8",
    )

    findings = lint(scan(root, mapping_from(DEFAULT_MAPPING)))

    assert [(item.path, item.rule) for item in findings] == [("untitled.md", "missing-title")]
