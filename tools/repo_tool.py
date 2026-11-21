"""Create repositories for work results where an administrator configured a forge."""

import json

from tools.registry import registry, tool_error


def available() -> bool:
    from hermes_cli.work.repos import configured
    return configured()


def _create(args: dict) -> str:
    from hermes_cli.work.repos import RepoError, create

    try:
        return json.dumps(create(args.get("repo")))
    except (OSError, RepoError) as exc:
        return tool_error(str(exc))
    except SystemExit as exc:
        # The shared work lock reports a frozen store through its CLI exit code.
        return tool_error(f"Work store unavailable (exit {exc.code}).")


registry.register(
    name="repo_create", toolset="repo", check_fn=available, handler=_create,
    schema={
        "name": "repo_create",
        "description": "Create a long-lived Git repository for an area's results. Returns its registered Git URL.",
        "parameters": {
            "type": "object",
            "properties": {"repo": {"type": "string", "description": "Work area ID and repository slug, e.g. A1/album"}},
            "required": ["repo"],
        },
    },
)
