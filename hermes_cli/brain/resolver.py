"""Which page type a file gets from the folder it sits in: an owner-editable mapping (``lina brain mapping`` prints
the default as YAML). A key without ``/`` matches a folder name at any depth, a key with ``/`` a folder path from
the imported directory's top; the deepest matching folder wins, and a file no key matches gets ``default``."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from hermes_cli.brain.page import PAGE_TYPES, alias_key

DEFAULT_MAPPING: dict = {
    "default": "concept",
    "folders": {
        "people": "person", "person": "person", "사람": "person", "인물": "person",
        "organizations": "organization", "companies": "organization", "조직": "organization", "회사": "organization",
        "projects": "project", "프로젝트": "project",
        "programs": "program", "areas": "program", "영역": "program",
        "concepts": "concept", "개념": "concept",
        "ideas": "idea", "아이디어": "idea",
        "sources": "source", "_sources": "source", "출처": "source", "자료": "source",
        "meetings": "meeting", "회의": "meeting",
        "writing": "writing", "글": "writing",
    },
}


class MappingError(ValueError):
    pass


@dataclass(frozen=True)
class Mapping:
    default: str
    folders: dict[str, str]

    def as_json(self) -> dict:
        return {"default": self.default, "folders": dict(self.folders)}

    def resolve(self, folder: str) -> tuple[str, str | None]:
        """(type, the key that decided it, or None when the default did) for a folder path like ``a/b``."""
        parts = [part for part in folder.split("/") if part]
        for depth in range(len(parts), 0, -1):
            prefix = alias_key("/".join(parts[:depth]))
            if prefix in self.folders:
                return self.folders[prefix], prefix
            name = alias_key(parts[depth - 1])
            if name in self.folders:
                return self.folders[name], name
        return self.default, None


def mapping_from(data: dict) -> Mapping:
    if not isinstance(data, dict) or not isinstance(data.get("folders", {}), dict):
        raise MappingError("a mapping is `default: <type>` and `folders: {<folder>: <type>}`")
    default = data.get("default", "concept")
    folders = {alias_key(str(key)).strip("/"): value for key, value in (data.get("folders") or {}).items()}
    wrong = sorted({str(value) for value in [default, *folders.values()] if value not in PAGE_TYPES})
    if wrong:
        raise MappingError(f"unknown page type {', '.join(wrong)}; one of {', '.join(PAGE_TYPES)}")
    return Mapping(default, folders)


def load_mapping(path: Path) -> Mapping:
    from ruamel.yaml.error import YAMLError

    import hermes_yaml
    try:
        data = hermes_yaml.safe_load(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise MappingError(f"cannot read the mapping {path}: {exc}") from exc
    except YAMLError as exc:
        raise MappingError(f"the mapping {path} is not YAML: {exc}") from exc
    return mapping_from(data or {})


def default_mapping_yaml() -> str:
    import hermes_yaml
    return hermes_yaml.safe_dump(DEFAULT_MAPPING, sort_keys=False)
