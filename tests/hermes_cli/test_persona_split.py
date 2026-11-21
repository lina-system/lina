"""Synthetic contracts for the lossless persona split in lina#825."""

import json
from collections import Counter
from hashlib import sha256
from itertools import groupby

import pytest

from hermes_cli.persona_split import UnmatchedDropError, split_personas


def _sha(text: str) -> str:
    return sha256(text.encode("utf-8")).hexdigest()


def test_split_preserves_the_multiset_and_order_of_each_homes_units() -> None:
    shared = "- Check stock\r\n  - Count crates\r\n"
    shared_variant = "- Check  stock\n  - Count crates\n"
    second = "- Lock the gate\r\n"
    private_list = "- Pack pears\r  - Put ripe ones on top\r"
    partly = "Keep the orchard schedule.\n"
    dropped = "Retire the old sign.\n"
    extras = "Count 배 trees by hand."
    sources = {
        "builder": (
            "# Orchard\r\n"
            "## Routine\r\n"
            f"{shared}{second}{shared}"
            f"## Packing\r{private_list}"
            f"## Old signs\n{dropped}"
            f"## Schedule\n{partly}\n{extras}"
        ),
        "operator": second + shared + "\n" + partly,
        "planner": shared_variant + second + "\n" + partly,
        "auditor": second + shared,
        "reviewer": "# Review\nCheck the receipts.\r\n",
        "empty": "",
    }
    original_units = {
        "builder": [shared, second, shared, private_list, dropped, partly, extras],
        "operator": [second, shared, partly],
        "planner": [shared_variant, second, partly],
        "auditor": [second, shared],
        "reviewer": ["Check the receipts.\r\n"],
        "empty": [],
    }

    result = split_personas(sources, {_sha("Retire the old sign."): "Owner retired it"})

    assert result.personas == {
        "builder": f"# Orchard\r\n## Routine\r\n## Packing\r{private_list}## Schedule\n{partly}\n{extras}",
        "operator": "\n" + partly,
        "planner": "\n" + partly,
        "auditor": "",
        "reviewer": sources["reviewer"],
        "empty": "",
    }
    assert result.common == {
        "builder": shared + "\n" + second + "\n" + shared,
        "operator": second + "\n" + shared,
        "planner": shared_variant + "\n" + second,
        "auditor": second + "\n" + shared,
        "reviewer": "",
        "empty": "",
    }
    assert result.ledger[0]["location"] == {"start_line": 3, "end_line": 4}
    assert result.ledger[0]["normalized_sha256"] == _sha("- Check stock - Count crates")

    # Re-read the emitted text and common payload, and recover actual dropped
    # text from the original line ranges. No production tokenizer is reused.
    for home, original in original_units.items():
        # In this fixture every surviving unit is bounded by a heading, a
        # blank line, or the file boundary.
        runs = groupby(
            result.personas[home].splitlines(keepends=True),
            key=lambda line: bool(line.strip()) and not line.startswith("#"),
        )
        persona_units = ["".join(lines) for is_unit, lines in runs if is_unit]
        common_lines = result.common[home].splitlines(keepends=True)
        common_runs = groupby(common_lines, key=lambda line: bool(line.strip()) and not line.startswith("#"))
        common_units = ["".join(lines) for is_unit, lines in common_runs if is_unit]
        source_lines = sources[home].splitlines(keepends=True)
        # Source positions count a heading shared by several rows or outputs once.
        headings = {}
        personal_heading_lines = set()
        common_heading_lines = set()
        dropped_units = []
        for row in result.ledger:
            if row["home"] != home:
                continue
            positions = row.get("heading_lines", [])
            headings.update((pos, source_lines[pos - 1]) for pos in positions)
            if row["disposition"] == "common":
                common_heading_lines.update(positions)
            elif row["disposition"] == "persona":
                personal_heading_lines.update(positions)
            else:
                location = row["location"]
                raw = "".join(source_lines[location["start_line"] - 1:location["end_line"]])
                assert row["raw_sha256"] == _sha(raw)
                assert row["reason"] == "Owner retired it"
                dropped_units.append(raw)
        personal_headings = [line for line in result.personas[home].splitlines(keepends=True) if line.startswith("#")]
        common_headings = [line for line in common_lines if line.startswith("#")]
        # Every heading above a common or personal unit is written to one of the two files.
        written = set(common_headings) | set(personal_headings)
        assert {source_lines[pos - 1] for pos in common_heading_lines | personal_heading_lines} <= written
        original_headings = [line for line in source_lines if line.startswith("#")]
        assert Counter(persona_units + common_units + dropped_units + list(headings.values())) == Counter(
            original + original_headings
        )

    assert result.summary["counts"] == {"common": 9, "persona": 6, "dropped": 1}
    assert result.summary["group_histogram"]["4"] == {
        "groups": 2,
        "occurrences": 9,
        "characters": 4 * len(shared) + len(shared_variant) + 4 * len(second),
    }
    assert result.summary["homes"]["builder"] == {
        "source_chars": len(sources["builder"]),
        "persona_chars": len(private_list) + len(partly) + len(extras),
        "common_chars": 2 * len(shared) + len(second),
        "dropped_chars": len(dropped),
        "membership": {"common": 3, "partly_shared": 1, "single": 3},
        "counts": {"common": 3, "persona": 3, "dropped": 1},
    }
    assert {
        key: result.summary[key]
        for key in ("distinct", "occurrences", "common", "near", "partly_shared", "ledger_lines")
    } == {"distinct": 7, "occurrences": 16, "common": 2, "near": 0, "partly_shared": 1, "ledger_lines": 16}
    assert json.loads(json.dumps(result.ledger)) == result.ledger
    assert json.loads(json.dumps(result.summary)) == result.summary
    assert extras not in json.dumps(result.summary)


def test_near_single_uses_maximum_directional_ratio_against_all_other_homes() -> None:
    single = "abcdefghijklmnoptide"
    shared = "abcdefghijklmnopdiet"
    lower_case_with_punctuation = "Mix all paint."
    sources = {
        "one": f"{single}\n\n{single}\n\n{single}\n\n{lower_case_with_punctuation}\n",
        "two": f"{shared}\n\nabcdefghijklmnzzxxxx\n\nmix all paint.\n",
        "three": f"{shared}\n\nMix all paint!\n",
        "four": shared,
        "empty": "",
    }

    result = split_personas(sources, {})

    rows = [row for row in result.ledger if row["normalized_sha256"] == _sha(single)]
    assert [row["location"] for row in rows] == [
        {"start_line": 1, "end_line": 1},
        {"start_line": 3, "end_line": 3},
        {"start_line": 5, "end_line": 5},
    ]
    # These strings score 0.85 in this order and 0.90 in reverse.
    expected_matches = [
        (home, _sha(shared), 0.90) for home in ("two", "three", "four")
    ]
    for row in rows:
        assert row["membership"] == "single"
        assert row["disposition"] == "persona"
        assert [
            (match["home"], match["normalized_sha256"], match["ratio"])
            for match in row["near_matches"]
        ] == expected_matches
    assert result.personas == sources
    assert result.common == dict.fromkeys(sources, "")
    shared_rows = [row for row in result.ledger if row["normalized_sha256"] == _sha(shared)]
    assert [row["membership"] for row in shared_rows] == ["partly_shared"] * 3
    assert result.summary["near"] == 4


def test_two_homes_are_partly_shared_even_when_every_home_has_the_unit() -> None:
    text = "- Store the spare key\n"
    variant = "- Store the  spare key\r\n"

    result = split_personas({"one": text, "two": variant}, {})

    assert [row["membership"] for row in result.ledger] == ["partly_shared"] * 2
    assert result.personas == {"one": text, "two": variant}


def test_headings_without_body_text_survive_in_the_persona() -> None:
    source = "# Policy\n## Pending\n## Active\n- Shared rule\n## Review\n"
    result = split_personas(dict.fromkeys(("one", "two", "three"), source), {})

    assert result.personas == dict.fromkeys(result.personas, "# Policy\n## Pending\n## Review\n")


def test_drops_override_common_membership_for_all_raw_variants() -> None:
    text = "- Store the spare key\n"
    variant = "- Store the  spare key\r\n"
    sources = {"one": text, "two": variant, "three": text}
    normalized_sha = _sha("- Store the spare key")

    dropped = split_personas(sources, {normalized_sha: "Retired rule"})

    assert dropped.personas == dict.fromkeys(sources, "")
    assert dropped.common == dict.fromkeys(sources, "")
    assert [
        (row["raw_sha256"], row["membership"], row["disposition"], row["reason"])
        for row in dropped.ledger
    ] == [
        (_sha(raw), "common", "dropped", "Retired rule") for raw in sources.values()
    ]
    assert dropped.summary["common"] == 1


def test_rejects_drop_digests_without_a_normalized_source_match() -> None:
    raw = "- Store the spare key\r\n"
    valid = _sha("- Store the spare key")
    unmatched = {_sha(raw): "Raw digest", _sha("Missing rule"): "Stale entry"}

    with pytest.raises(UnmatchedDropError) as error:
        split_personas({"one": raw}, {valid: "Retired rule", **unmatched})

    assert error.value.digests == tuple(sorted(unmatched))


def test_only_dash_space_starts_a_new_item_and_indented_items_stay_nested() -> None:
    source = "- Top item\r\n  - Nested item\n---\n-\tcontinued\n-next line\n-\n"

    result = split_personas({"one": source + "- Next item"}, {})

    assert [row["raw_sha256"] for row in result.ledger] == [_sha(source), _sha("- Next item")]


@pytest.mark.parametrize("block", [
    "Example:\n```text\n- Do not run this command\n```\n",
    "Example:\n   ````text\n\n```\n# Still code\n`````\n",
    "Example:\n```\n~~~\n\n- Unclosed\n",
    "Example:\n  ~~~text\n- Code\n\n  ~~~~\n",
])
def test_fenced_example_stays_one_unit(block):
    sources = {name: f"- Private {name}\n\n{block}" for name in ("one", "two", "three")}
    result = split_personas(sources, {})
    moved = [row for row in result.ledger if row["disposition"] == "common"]
    assert [row["raw_sha256"] for row in moved] == [_sha(block)] * 3
    for name in sources:
        assert result.personas[name] == f"- Private {name}\n\n"


def test_near_matching_preserves_difflib_default_popular_character_handling() -> None:
    repeated = "a" * 220

    result = split_personas({"one": f"x{repeated}y", "two": f"z{repeated}w"}, {})

    assert result.summary["near"] == 0
    assert [row["near_matches"] for row in result.ledger] == [[], []]


def test_removal_does_not_join_a_bare_cr_to_a_later_lf_separator() -> None:
    source = "Keep this line.\r- Retire this item.\r\n\nKeep this paragraph."

    result = split_personas({"one": source}, {_sha("- Retire this item."): "Retired"})

    lines = result.personas["one"].splitlines(keepends=True)
    assert [line for line in lines if line.strip()] == [
        "Keep this line.\r",
        "Keep this paragraph.",
    ]
    assert len(lines) == 3
    assert not lines[1].strip()


def test_headings_naming_one_home_never_enter_the_common_file() -> None:
    sources = {
        name: f"# {name.title()}\n## Rules\n- Lock up\n- Log out\n## {name} only\n- Local {name}\n"
        for name in ("one", "two", "three")
    }

    result = split_personas(sources, {})

    assert len(set(result.common.values())) == 1
    assert result.common["one"] == "## Rules\n- Lock up\n\n- Log out\n"
    for name in sources:
        assert result.personas[name] == f"# {name.title()}\n## {name} only\n- Local {name}\n"


def test_a_home_title_with_only_common_units_stays_in_the_persona() -> None:
    sources = {name: f"# {name}\n- Lock up\n" for name in ("one", "two", "three")}

    result = split_personas(sources, {})

    assert len(set(result.common.values())) == 1
    assert result.common["one"] == "- Lock up\n"
    assert result.personas == {name: f"# {name}\n" for name in sources}
