"""Discover in-tree terminal providers without a backend-name import list."""

import ast
import importlib
from functools import cache
from pathlib import Path


@cache
def discover_environment_providers() -> None:
    """Import modules that explicitly register a terminal environment provider.

    Inspect declarations rather than importing every environment helper: optional SDKs
    remain lazy, and removing a provider file also removes its registration.
    """
    for path in sorted(Path(__file__).parent.glob("*.py")):
        tree = ast.parse(path.read_bytes(), filename=str(path))
        if any(isinstance(node, ast.Expr) and isinstance(node.value, ast.Call)
               and isinstance(node.value.func, ast.Name)
               and node.value.func.id == "register_provider" for node in tree.body):
            importlib.import_module(f"tools.environments.{path.stem}")
