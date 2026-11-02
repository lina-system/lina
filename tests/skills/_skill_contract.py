"""Shared document contract for bundled skills (skills/AGENTS.md, authoring standards 1, 2, 5).

A skill is instructions, so its tests check what a model would act on: the frontmatter the index
and loader read, the section order, and that every path, CLI command and tool the body names exists
in this checkout. Per-skill files add the obligation that must not break for that skill.
"""

from __future__ import annotations

import argparse
import re
from dataclasses import dataclass
from functools import cache
from pathlib import Path

from agent.skill_utils import SKILL_PROMPT_DESC_LIMIT, parse_frontmatter

REPO = Path(__file__).resolve().parents[2]
SECTIONS = ("When to Use", "Prerequisites", "How to Run", "Quick Reference", "Procedure", "Pitfalls",
            "Verification")
REQUIRED_FIELDS = ("name", "description", "version", "author", "license", "platforms")
_FORBIDDEN = re.compile(
    r"hermes-agent\.nousresearch\.com|image_generate|teams-pipeline|docker sandbox|optional-skills/"
    r"|/home/(?!runner\b)[a-z0-9_-]+/|/Users/[A-Za-z]", re.IGNORECASE)
_MARKETING = re.compile(r"\b(?:powerful|comprehensive|seamless|revolutionary|cutting-edge|state-of-the-art)\b",
                        re.IGNORECASE)
_QUOTED = re.compile(r"`[^`\n]*`|\"[^\"\n]*\"|'[^'\n]*'|“[^”\n]*”")
_FENCE = re.compile(r"^\s*(`{3,}|~{3,})")
_SKILL_RELATIVE = re.compile(r"(?<![\w./-])((?:references|scripts|templates)/[\w./-]*\w)")
_CLI = re.compile(r"\blina ((?:[a-z][a-z-]*)(?: [a-z][a-z-]*)?)")


@dataclass(frozen=True)
class Skill:
    name: str
    directory: Path
    frontmatter: dict
    body: str


def load(name: str) -> Skill:
    found = sorted(REPO.glob(f"skills/*/{name}/SKILL.md"))
    assert len(found) == 1, f"{name}: expected one bundled SKILL.md, found {found}"
    frontmatter, body = parse_frontmatter(found[0].read_text(encoding="utf-8"))
    return Skill(name, found[0].parent, frontmatter, body)


def prose_lines(body: str):
    fence = None
    for line in body.splitlines():
        match = _FENCE.match(line)
        if match and (fence is None or line.strip().startswith(fence)):
            fence = None if fence else match.group(1)
            continue
        if fence is None:
            yield line


def sections(body: str) -> list[str]:
    return [line[3:].strip() for line in prose_lines(body) if line.startswith("## ")]


def assert_frontmatter(skill: Skill) -> None:
    fm = skill.frontmatter
    missing = [field for field in REQUIRED_FIELDS if field not in fm]
    assert not missing, f"{skill.name}: missing {missing}"
    assert fm["name"] == skill.directory.name
    description = str(fm["description"])
    assert len(description) <= SKILL_PROMPT_DESC_LIMIT and description.rstrip().endswith("."), description
    hermes = (fm.get("metadata") or {}).get("hermes") or {}
    assert hermes.get("tags"), f"{skill.name}: no tags"
    assert hermes.get("category") == skill.directory.parent.name


def assert_section_order(skill: Skill, *, complete: bool) -> None:
    """The modern sections present appear in the standard order; ``complete`` requires all of them."""
    present = [title for title in sections(skill.body) if title in SECTIONS]
    assert present == [title for title in SECTIONS if title in present], present
    if complete:
        assert present == list(SECTIONS), present
        assert next(line for line in prose_lines(skill.body) if line.startswith("# ")).endswith(" Skill")


def assert_no_forbidden_text(skill: Skill) -> None:
    text = (skill.directory / "SKILL.md").read_text(encoding="utf-8")
    for path in sorted(p for p in skill.directory.rglob("*.md") if p.name != "SKILL.md"):
        text += path.read_text(encoding="utf-8")
    hits = sorted({m.group(0) for m in _FORBIDDEN.finditer(text)}
                  | {m.group(0) for m in _MARKETING.finditer(_QUOTED.sub("", skill.body))})
    assert not hits, f"{skill.name}: {hits}"


def assert_referenced_files_exist(skill: Skill) -> list[str]:
    """Every ``references/``, ``scripts/`` or ``templates/`` path resolves in the skill or the repo."""
    refs = sorted(set(_SKILL_RELATIVE.findall(skill.body)))
    missing = [ref for ref in refs if not ((skill.directory / ref).exists() or (REPO / ref).exists())]
    assert not missing, f"{skill.name}: {missing}"
    return refs


@cache
def _cli_tree() -> dict[str, frozenset[str]]:
    from hermes_cli.main import _build_cli_parser

    parser, _ = _build_cli_parser()

    def children(p: argparse.ArgumentParser) -> dict[str, argparse.ArgumentParser]:
        for action in p._actions:
            if isinstance(action, argparse._SubParsersAction):
                return dict(action.choices)
        return {}

    return {name: frozenset(children(sub)) for name, sub in children(parser).items()}


def assert_cli_commands_exist(skill: Skill) -> list[str]:
    """Every ``lina <command> [<subcommand>]`` the body names is a parser this checkout builds."""
    tree = _cli_tree()
    named = sorted(set(_CLI.findall(skill.body)))
    unknown = []
    for phrase in named:
        command, _, sub = phrase.partition(" ")
        if command not in tree or (sub and tree[command] and sub not in tree[command]
                                   and not sub.startswith("-")):
            unknown.append(phrase)
    assert not unknown, f"{skill.name}: {unknown}"
    return named


def registered_tools() -> frozenset[str]:
    from tools.registry import discover_builtin_tools, registry

    discover_builtin_tools()
    return frozenset(registry.get_all_tool_names())


def assert_tools_registered(skill: Skill, tools: tuple[str, ...]) -> None:
    """The tools the skill tells the model to call are named in its body and registered here."""
    from tools.registry import registry

    absent = [tool for tool in tools if f"`{tool}" not in skill.body]
    assert not absent, f"{skill.name}: body no longer names {absent}"
    unknown = [tool for tool in tools if tool not in registered_tools()]
    assert not unknown, f"{skill.name}: not registered tools {unknown}"
    for tool, args in re.findall(r"\b([a-z_]+)\(((?:[a-z_]+=[^,()]*,?\s*)+)\)", skill.body):
        if tool not in registered_tools():
            continue
        params = set(registry.get_entry(tool).schema["parameters"].get("properties", {}))
        bad = [name for name in re.findall(r"([a-z_]+)=", args) if name not in params]
        assert not bad, f"{skill.name}: {tool}() has no parameter {bad}"


def assert_contract(skill: Skill, *, complete_sections: bool) -> None:
    assert_frontmatter(skill)
    assert_section_order(skill, complete=complete_sections)
    assert_no_forbidden_text(skill)
    assert_referenced_files_exist(skill)
    assert_cli_commands_exist(skill)
