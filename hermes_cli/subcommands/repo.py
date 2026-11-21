"""The repository creation command."""

import json


def _create(args) -> None:
    from hermes_cli.work.repos import RepoError, create

    try:
        result = create(args.repo)
    except (OSError, RepoError) as exc:
        raise SystemExit(str(exc)) from exc
    print(json.dumps(result))


def build_repo_parser(subparsers) -> None:
    parser = subparsers.add_parser("repo", help="Manage an area's Git repositories")
    create = parser.add_subparsers(required=True).add_parser("create", help="Create or return a work repository")
    create.add_argument("repo", metavar="AREA/name", help="Work area ID and repository slug, e.g. A1/album")
    create.set_defaults(func=_create)
