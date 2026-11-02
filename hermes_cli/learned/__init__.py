"""Daily learning review over upstream records."""

def main(argv: list[str] | None = None) -> int:
    from hermes_cli.learned.cli import main as run
    return run(argv)
