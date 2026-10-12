# Releases

Development changes join `dev` through [integration](integration.md). The owner approves the release of one exact, verified commit; `scripts/release.mjs` publishes it and fast-forwards `main` to that commit. Every release and deployment needs the owner's approval.

## Run

```sh
node scripts/release.mjs --commit <sha> --tag <vX.Y.Z> --notes-file <file> [--publish]
```

- `--commit`: the full 40-character commit SHA, on `dev`.
- `--tag`: a version tag such as `v0.1.0` or `v0.1.0-rc.1`.
- `--notes-file`: a file with user-facing release notes.

Without `--publish`, the script validates and changes no tag, release, or branch. Pass `--publish` only when the owner has approved that release.

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

## Distribution

The main installs LINA from the GitHub release, downloaded with the installing person's own `gh` authentication, which needs read access to this repository. Node binaries and updates reach the other devices from the main LINA Core over the tailnet ([runtime.md](../design/runtime.md#updates)).

## Recovery

Tags are immutable. A rerun reuses an existing tag only when it identifies the same commit. The script never rewrites a tag or an existing published release.

If publication succeeds but advancing `main` fails, the release still exists. Inspect the failure and rerun with the same tag and commit after correcting it. An older or divergent commit cannot replace `main`; moving it backwards is not a recovery mechanism.
