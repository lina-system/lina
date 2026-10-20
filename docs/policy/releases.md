# Releases

Development changes join `dev` through [integration](integration.md). The owner approves the release of one exact, verified commit; `scripts/release.mjs` publishes it and fast-forwards `main` to that commit. Every release and deployment needs the owner's approval.

## Run

```sh
node scripts/release.mjs --commit <sha> --tag <v0.0.1-alpha.YYYYMMDD.N> --notes-file <file> [--publish]
```

- `--commit`: the full 40-character commit SHA, on `dev`.
- `--tag`: the version tag of [versioning.md](../design/versioning.md), such as `v0.0.1-alpha.20261020.1`: the date of tagging in UTC, then the number of the release of that day from 1. A tag carries no build metadata (`+`).
- `--notes-file`: a file with user-facing release notes.

Without `--publish`, the script validates and changes no tag, release, or branch. Pass `--publish` only when the owner has approved that release.

## Generate release notes

```sh
node scripts/release-notes.mjs --from <tag or root> --to <sha> --out <file>
```

Use the previous release tag as `--from`, or the literal `root` for the first release. The range includes commits reachable from `--to` and excludes those already reachable from `--from`; `root` includes all history through `--to`. Fetch the relevant history and tags before running it. The script reads merge commits whose subjects start with `Merge pull request #N` and asks `gh pr view N --json title,labels` for each distinct pull request. It uses the current GitHub title and labels, so `gh` needs read access to this repository.

Each pull request appears once as `title (#number)`, under these headings in order: Breaking changes, Features, Bug fixes, Decisions, Other. A title with `!` immediately before its first colon (such as `feat!:` or `fix(core)!:`) goes under Breaking changes regardless of its labels. Otherwise the exact type labels `feature`, `bug` and `decision` select the group, in that priority order when several are present. A pull request with none of those labels goes under Other. Empty groups are omitted; entries within each group follow Git's oldest-first topological order.

An empty range, a range without matching pull request merges, an unknown revision or a failed metadata lookup exits non-zero and leaves any existing output file unchanged. Successful generation writes the Markdown file for `scripts/release.mjs --notes-file`. It adds no release gate; inspect the notes and include any migration or recovery guidance the release needs before requesting approval. The generator's tests use a stub `gh` and local Git repositories without network access.

## Validate

Every run, with or without `--publish`, fetches `dev`, `main` and the tags from the remote and checks that:

- the commit belongs to `dev`;
- `main` is an ancestor of the commit, so `main` fast-forwards to it;
- the tag is a valid version tag, and if it already exists locally or on the remote, it identifies this commit;
- the notes are not empty;
- `foundation` passed on this exact commit. The script reads the evidence note in `refs/notes/foundation` ([foundation.md](foundation.md)); when the note is missing or does not show a passing run of this commit, it runs `foundation --full` on the commit in a clean worktree.

Any failed check stops the run before anything is built or published.

## Publish

With `--publish`, the script runs two separate stages.

1. **Build:** in a clean worktree of the exact commit, with no credentials in its environment, it builds the release artifacts and their release manifest ([runtime.md](../design/runtime.md#release-manifest)). This stage verifies modules and installs packages as the [dependency policy](dependencies.md) requires.
2. **Publication:** with the owner's local Git and GitHub credentials, it pushes the tag, creates the GitHub release for the tag in this private repository with the notes, the artifacts and the manifest, and fast-forwards `main` to the commit and pushes it. This stage fetches no modules and installs no packages.

A release carries the source at its tag, the repository's [license](../../LICENSE) and the [third-party notices](../../THIRD-PARTY-NOTICES.md). It publishes no package to any registry and deploys no service.

When building `lina` and `lina-node`, the build stage supplies these string variables with `go build -ldflags "-X <name>=<value> ..."`:

| Name | Value |
| --- | --- |
| `github.com/thisisjun786/lina/internal/buildinfo.Version` | The release tag without its leading `v`, `0.0.1-alpha.YYYYMMDD.N`, with no `+` metadata |
| `github.com/thisisjun786/lina/internal/buildinfo.Commit` | The verified source commit SHA |
| `github.com/thisisjun786/lina/internal/buildinfo.Date` | The build date in UTC, `YYYY-MM-DD` |

Both binaries report these fields through `version` as specified in [versioning.md](../design/versioning.md#what-a-binary-reports). The source defaults remain a development version and do not change for each release. The current release script packages source; wiring binary artifacts into that script is separate from the commands' metadata support.

## Distribution

The main installs LINA from the GitHub release, downloaded with the installing person's own `gh` authentication, which needs read access to this repository. Node binaries and updates reach the other devices from the main LINA Core over the tailnet ([runtime.md](../design/runtime.md#updates)).

## Recovery

Tags are immutable. A rerun reuses an existing tag only when it identifies the same commit. The script never rewrites a tag or an existing published release.

If publication succeeds but advancing `main` fails, the release still exists. Inspect the failure and rerun with the same tag and commit after correcting it. An older or divergent commit cannot replace `main`; moving it backwards is not a recovery mechanism.
