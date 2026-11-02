"""``work:`` in the LINA root's ``config.yaml``.

```yaml
work:
  repos: [owner/name]            # what `lina work next|status` look at when --repo is not given
  sprint: M0 S3                  # the current sprint; only the owner moves it (`lina work sprint set`)
  base: dev                      # the branch pull requests go into
  dev_workflow: CI               # the push workflow whose latest run says whether dev is green
  runner_labels: {grok-cursor: grok}   # issue label -> lina worker runner (default: a label names its runner)
  contract_scopes: [store, config]     # title scopes that never run twice at once (feat(<scope>): ...)
  author: Name <id+name@users.noreply.github.com>   # the only commit author a landed branch may carry
  forbidden_patterns: ["private-host", "/home/someone"]   # regexes refused in a landed branch's added lines
  followup_two_level: [scripts/ci]     # paths whose follow-up area is two directories deep
```
"""
from __future__ import annotations

import re
from pathlib import Path

DEFAULT_SCOPES = ("store", "install", "models", "cli", "home", "config", "service", "migrate")
_SPRINT = re.compile(r"^\s*(?:(M\d+)\s+)?S(\d+)\s*$", re.IGNORECASE)
_ISSUE_SPRINT = re.compile(r"^\s*Sprint:\s*(?:(M\d+)\s+)?S(\d+)\b", re.MULTILINE | re.IGNORECASE)


def lina_root(base: Path | None = None) -> Path:
    from hermes_cli.lina_ocx import lina_root as root
    return base or root()


def config_path(base: Path | None = None) -> Path:
    return lina_root(base) / "config.yaml"


def _doc(base: Path | None) -> dict:
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    path = config_path(base)
    doc = read_config_doc_readonly(path) if config_exists(path) else None
    return doc if isinstance(doc, dict) else {}


def section(base: Path | None = None) -> dict:
    value = _doc(base).get("work")
    return value if isinstance(value, dict) else {}


def _list(value) -> list[str]:
    return [str(v) for v in value] if isinstance(value, list) else []


def repos(base: Path | None = None) -> list[str]:
    return _list(section(base).get("repos"))


def base_branch(base: Path | None = None) -> str:
    return str(section(base).get("base") or "dev")


def dev_workflow(base: Path | None = None) -> str:
    return str(section(base).get("dev_workflow") or "CI")


def contract_scopes(base: Path | None = None) -> tuple[str, ...]:
    return tuple(_list(section(base).get("contract_scopes"))) or DEFAULT_SCOPES


def author(base: Path | None = None) -> str | None:
    value = section(base).get("author")
    return str(value) if value else None


def forbidden_patterns(base: Path | None = None) -> list[str]:
    return _list(section(base).get("forbidden_patterns"))


def followup_two_level(base: Path | None = None) -> tuple[str, ...]:
    return tuple(_list(section(base).get("followup_two_level")))


def runner_labels(base: Path | None = None) -> dict[str, str]:
    """Issue label -> runner, in preference order: omo first, then every runner in ``worker.runners``, then the
    explicit ``work.runner_labels`` (which may point a label at another runner name)."""
    from hermes_cli.worker import runners
    from hermes_cli.worker import settings as worker_settings
    table = {name: name for name in runners.specs(worker_settings.section(lina_root(base)))}
    explicit = section(base).get("runner_labels")
    if isinstance(explicit, dict):
        table.update({str(label): str(runner) for label, runner in explicit.items()})
    return table


def parse_sprint(value: str | None) -> tuple[str | None, int] | None:
    """``"M0 S3"`` -> ``("M0", 3)``, ``"S3"`` / ``"3"`` -> ``(None, 3)``; None when it is not a sprint."""
    text = str(value or "").strip()
    if text.isdigit():
        text = f"S{text}"
    match = _SPRINT.match(text)
    return (match[1].upper() if match[1] else None, int(match[2])) if match else None


def sprint_label(parsed: tuple[str | None, int]) -> str:
    return f"{parsed[0]} S{parsed[1]}" if parsed[0] else f"S{parsed[1]}"


def sprint(base: Path | None = None) -> tuple[str | None, int] | None:
    return parse_sprint(section(base).get("sprint"))


def set_sprint(value: str, base: Path | None = None) -> str:
    parsed = parse_sprint(value)
    if parsed is None:
        raise ValueError(f"not a sprint: {value!r} (write S3 or M0 S3)")
    from hermes_cli.config_backend import write_config_key
    label = sprint_label(parsed)
    write_config_key(config_path(base), "work.sprint", label)
    return label


def issue_sprint(body: str | None) -> tuple[str | None, int] | None:
    match = _ISSUE_SPRINT.search(body or "")
    return (match[1].upper() if match[1] else None, int(match[2])) if match else None


def in_sprint(body: str | None, current: tuple[str | None, int] | None) -> bool:
    found = issue_sprint(body)
    if current is None or found is None or found[1] != current[1]:
        return False
    return current[0] is None or found[0] is None or current[0] == found[0]
