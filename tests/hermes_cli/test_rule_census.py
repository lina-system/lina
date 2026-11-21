import json
import subprocess
import sys
from pathlib import Path

import pytest


@pytest.fixture
def copy_root(tmp_path):
    (tmp_path / ".standing-rule-copy").touch()
    return tmp_path


def _write(root: Path, relative: str, data: bytes) -> Path:
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)
    return path


def test_copied_sources_are_ordered_and_owner_list_uses_the_same_numbers(copy_root):
    from hermes_cli.rule_census import candidates, owner_list

    _write(copy_root, "config.yaml", b"undo:\n  rules: [shared/*.md]\n")
    _write(copy_root, "shared/rules.md", b"# Shared\n* Never include footnotes.\n")
    home = copy_root / "profiles/reader"
    _write(home, "SOUL.md", b"# Persona\nI am a botanist.\n- Always write concise replies.\n")
    _write(home, "SOUL.overlay.md", b"Never migrate this instruction.\n")
    _write(home, "SOUL.self.md", b"Always keep this note in place.\n")
    _write(home, "memories/USER.md", b"Owns a bicycle.\n\xc2\xa7\n- Please always use plain text.\n")
    _write(home, "memories/MEMORY.md", b"The garden has three ponds.\nNever include filler.\n")
    _write(home, "user-notes/preferences.md", b"Always use plain language.\n")
    _write(home, "skills/review/SKILL.md", b"---\nname: review\n---\nOnly surface posts that mention me.\n")
    _write(home, "cron/jobs.json", b'{"jobs":[{"name":"Never count this name","prompt":"Skip archived posts."}]}')
    _write(home, "room-lists.json", b'{"rooms":["room-for-reviews"]}')
    _write(home, "closed-items.txt", b"archived-topic\n")
    _write(home, "script-constants.json", b'["muted-source"]')
    _write(home, "honcho/conclusions.jsonl", b'{"id":"sample","content":"Never include signoffs."}\n')

    items = candidates(copy_root, "reader")

    assert [(item.place, item.agent, item.kind) for item in items] == [
        ("shared", "reader", "conduct"),
        ("soul", "reader", "conduct"),
        ("user", "reader", "conduct"),
        ("memory", "reader", "conduct"),
        ("user_notes", "reader", "conduct"),
        ("skill", "reader", "filter"),
        ("job", "reader", "filter"),
        ("room_list", "reader", "filter"),
        ("closed_list", "reader", "filter"),
        ("script_constant", "reader", "filter"),
        ("honcho", "reader", "conduct"),
    ]
    assert owner_list(items, limit=2) == (
        "1. [shared · all agents] Never include footnotes.\n"
        "2. [soul · profile reader] Always write concise replies."
    )
    assert "[room list · profile reader · exclude room] room-for-reviews" in owner_list(items)
    assert [item.filter_match for item in items if item.kind == "filter"] == [
        {"mentions_owner": False},
        {"kind": "archived"},
        {"room": "room-for-reviews"},
        {"item": "archived-topic"},
        {"source": "muted-source"},
    ]


def test_owner_list_caps_visibility_at_twelve(copy_root):
    from hermes_cli.rule_census import candidates, owner_list

    lines = [f"Never use term {index}." for index in range(13)]
    _write(copy_root, "SOUL.md", "\n".join(lines).encode())
    rendered = owner_list(candidates(copy_root, "default"), limit=20)

    assert rendered.splitlines() == [
        f"{number}. [soul · profile default] {line}" for number, line in enumerate(lines[:12], 1)
    ]


def test_scope_prefixes_preserve_raw_wording_and_exact_room_filters(copy_root):
    from hermes_cli.rule_census import candidates, owner_list

    shared = "Everyone, always write concise replies."
    shared_ko = "모두, 항상 짧게 답해."
    other = "Agent archivist, never use jargon."
    per_agent = "너는 항상 짧게 답해."
    room = "In the #garden room, only messages that mention me."
    room_ko = "#greenhouse 방에서는 나를 언급한 메시지만 보여줘."
    _write(
        copy_root, "SOUL.md",
        "\n".join((shared, shared_ko, other, per_agent, room, room_ko,
                   "In this room, only messages that mention me.",
                   "이 방에서는 나를 언급한 메시지만 보여줘.")).encode(),
    )

    items = candidates(copy_root, "default")

    assert [(item.statement, item.scope, item.place_ref, item.filter_match) for item in items] == [
        (shared, "all", None, None),
        (shared_ko, "all", None, None),
        (other, "agent", None, None),
        (per_agent, "agent", None, None),
        (room, "place", "channel:#garden", {"mentions_owner": False, "room": "#garden"}),
        (room_ko, "place", "channel:#greenhouse", {"mentions_owner": False, "room": "#greenhouse"}),
    ]
    assert "[soul · profile archivist]" in owner_list(items)
    assert "[soul · channel:#garden · only items mentioning me]" in owner_list(items)


def test_legacy_root_notes_include_distinct_sources_without_duplicate_copies(copy_root):
    from hermes_cli.rule_census import candidates

    current = b"Never use filler."
    canonical = _write(copy_root, "memories/USER.md", current)
    _write(copy_root, "USER.md", current)
    _write(copy_root, "memories/MEMORY.md", b"The garden has a pond.")
    legacy = _write(copy_root, "MEMORY.md", b"Always respond in plain text.")

    assert [(item.path, item.place) for item in candidates(copy_root, "default")] == [
        (canonical, "user"), (legacy, "memory"),
    ]


def test_filter_grammar_refuses_commands_and_unknown_targets(copy_root):
    from hermes_cli.rule_census import candidates

    _write(
        copy_root,
        "cron/jobs.json",
        json.dumps({"jobs": [{"prompt": (
            "Skip posts from room #garden.\n"
            "Skip messages from room #garden && exec sample-command.\n"
            "Never surface posts if running sample-command returns a nonzero status.\n"
            "Exclude items from source MY-Feed.\n"
            "Agent archivist, skip closed messages.\n"
            "Agent default, skip closed messages."
        )}]}).encode(),
    )
    _write(copy_root, "room-lists.json", b'["room-one", "room-one || sample-command"]')

    assert [item.filter_match for item in candidates(copy_root, "default")] == [
        {"room": "#garden"}, {"source": "MY-Feed"}, {"kind": "closed"}, {"room": "room-one"},
    ]


def test_registered_script_constants_are_read_without_running_the_script(copy_root):
    from hermes_cli.rule_census import candidates, remove_candidates

    original = (
        '# 녹색\nROOM_EXCLUSIONS = ["#orchard", # keep this comment, too\n'
        '    "#garden",]\nUNRELATED = ["leave-in-place"]\nraise AssertionError("offline only")\n'
    ).encode()
    _write(copy_root, "scripts/rule-filters/garden.py", original)
    _write(copy_root, "scripts/unregistered.py", b'MUTED_SOURCES = ["unregistered"]\n')

    items = candidates(copy_root, "default")

    assert [(item.place, item.filter_match) for item in items] == [
        ("script_constant", {"room": "#orchard"}), ("script_constant", {"room": "#garden"}),
    ]
    assert remove_candidates(original, [items[1]]) == original.replace(b'"#garden",', b"")


@pytest.mark.parametrize(
    ("relative", "original", "selected", "expected"),
    [
        (
            "SOUL.md",
            b"\xef\xbb\xbf# Role\r\nNever use filler.\r\nI am a botanist.\r\nNever use filler.",
            [1],
            b"\xef\xbb\xbf# Role\r\nNever use filler.\r\nI am a botanist.\r\n",
        ),
        (
            "cron/jobs.json",
            rb'{"jobs": [ {"prompt": "Never invent facts.\u000D\nKeep \u0061 notes.\\nStill here.\nSkip archived posts.", "extra": 4 } ]}',
            [0, 1],
            rb'{"jobs": [ {"prompt": "Keep \u0061 notes.\\nStill here.\n", "extra": 4 } ]}',
        ),
        (
            "room-lists.json",
            b'{"rooms": ["keep" , "remove-a",  "remove-b"], "owner": "keep"}\n',
            [1, 2],
            b'{"rooms": ["keep"    ], "owner": "keep"}\n',
        ),
        (
            "honcho/conclusions.jsonl",
            b'{"id":1, "content":"Never include signoffs."}\r\n{"id":2,"content":"Lives in a cabin."}\r\n',
            [0],
            b'{"id":2,"content":"Lives in a cabin."}\r\n',
        ),
        (
            "honcho/conclusions.json",
            b'\xef\xbb\xbf{"conclusions":[{"content":"Never include filler."},{"content":"Lives in a cabin."}]}',
            [0],
            b'\xef\xbb\xbf{"conclusions":[{"content":"Lives in a cabin."}]}',
        ),
    ],
    ids=["duplicate-line", "escaped-job-prompt", "array-suffix", "json-lines", "json-bom"],
)
def test_removal_keeps_unselected_source_bytes(copy_root, relative, original, selected, expected):
    from hermes_cli.rule_census import candidates, remove_candidates

    path = _write(copy_root, relative, original)
    found = candidates(copy_root, "default")
    selection = [found[index] for index in selected]

    assert remove_candidates(original, selection) == expected
    assert path.read_bytes() == original
    with pytest.raises(ValueError):
        remove_candidates(original + b"\n", selection)


@pytest.mark.platforms("posix")
def test_discovery_does_not_follow_external_paths_or_symlinks(tmp_path, monkeypatch):
    from hermes_cli.rule_census import candidates

    outside = tmp_path / "outside"
    external = _write(outside, "SOUL.md", b"Never include this external text.\n")
    root = tmp_path / "copy"
    _write(root, ".standing-rule-copy", b"")
    _write(root, "config.yaml", f"undo:\n  rules: [{external}, ../outside/*.md, '~/SOUL.md', shared/*.md]\n".encode())
    _write(root, "shared/local.md", b"Never include filler.\n")
    (root / "shared/link.md").symlink_to(external)
    (root / "skills").symlink_to(outside, target_is_directory=True)
    (root / "SOUL.md").symlink_to(root / "shared/local.md")
    link = tmp_path / "link"
    link.symlink_to(root, target_is_directory=True)
    monkeypatch.setenv("HOME", str(outside))
    monkeypatch.setenv("HERMES_HOME", str(outside))

    assert [item.path for item in candidates(root, "default")] == [root / "shared/local.md"]
    with pytest.raises(ValueError):
        candidates(link, "default")
    with pytest.raises(ValueError):
        candidates(root, "../outside")


def test_broad_shared_globs_cannot_promote_self_notes_or_snapshot_metadata(copy_root):
    from hermes_cli.rule_census import candidates

    _write(copy_root, "config.yaml", b"undo:\n  rules: ['**/*.md', 'undo/*.json', 'checkpoints/**/*.md']\n")
    legitimate = _write(copy_root, "shared/guidance.md", b"Never include signoffs.")
    for relative in (
        "SOUL.self.md", "SOUL.overlay.md", "profiles/other/SOUL.self.md",
        "undo/snapshot.md", "checkpoints/tree/payload.md", "profiles/other/checkpoints/payload.md",
    ):
        _write(copy_root, relative, b"Never include filler.")
    _write(copy_root, "undo/snapshot.json", b"\xff")

    assert [item.path for item in candidates(copy_root, "default")] == [legitimate]


@pytest.mark.platforms("posix")
def test_multiply_linked_sources_are_never_read(tmp_path, monkeypatch):
    from hermes_cli.rule_census import candidates

    external = _write(tmp_path, "outside.md", b"Never include filler.")
    copy = tmp_path / "copy"
    _write(copy, ".standing-rule-copy", b"")
    _write(copy, "config.yaml", b"undo:\n  rules: ['shared/*.md']\n")
    legitimate = _write(copy, "shared/local.md", b"Never include signoffs.")
    linked = {copy / "SOUL.md", copy / "shared/alias.md"}
    for path in linked:
        path.hardlink_to(external)
    read_bytes = Path.read_bytes

    def read_unaliased(path):
        assert path not in linked, "the census read a hardlinked source"
        return read_bytes(path)

    monkeypatch.setattr(Path, "read_bytes", read_unaliased)

    assert [item.path for item in candidates(copy, "default")] == [legitimate]


def test_module_cli_only_reports_counts_even_when_a_source_is_invalid(copy_root, caplog):
    payload = b"Never repeat the blue test phrase."
    _write(copy_root, "SOUL.md", payload)
    _write(copy_root, "profiles/other/SOUL.self.md", payload)

    def invoke():
        return subprocess.run(
            [sys.executable, "-B", "-m", "hermes_cli.rule_census", str(copy_root)],
            check=False,
            capture_output=True,
        )

    census = invoke()
    assert census.returncode == 0
    counts = json.loads(census.stdout)
    assert counts["default"]["soul"] == 1
    assert counts["other"]["self_note"] == 1
    assert census.stderr == b""

    _write(copy_root, "cron/jobs.json", payload)
    invalid = invoke()
    assert invalid.returncode != 0
    assert b"Traceback" not in invalid.stderr
    assert payload not in census.stdout + invalid.stdout + invalid.stderr + caplog.text.encode()
    assert {path.relative_to(copy_root).as_posix() for path in copy_root.rglob("*") if path.is_file()} == {
        ".standing-rule-copy",
        "SOUL.md",
        "profiles/other/SOUL.self.md",
        "cron/jobs.json",
    }


def test_discovery_requires_an_empty_opt_in_file(tmp_path):
    from hermes_cli.rule_census import COPY_MARKER, copy_root

    _write(tmp_path, "SOUL.md", b"Never include filler.\n")
    with pytest.raises(ValueError, match="standing-rule-copy"):
        copy_root(tmp_path)
    marker = _write(tmp_path, COPY_MARKER, b"some unrelated file")
    with pytest.raises(ValueError, match="standing-rule-copy"):
        copy_root(tmp_path)
    marker.write_bytes(b"")
    assert copy_root(tmp_path) == tmp_path
