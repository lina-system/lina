"""Reference-only undo journal backed by LINA checkpoints."""

from __future__ import annotations


def main(argv: list[str] | None = None) -> int:
    from hermes_cli.undo.cli import main as run

    return run(argv)
