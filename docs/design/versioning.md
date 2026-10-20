# Versioning

This document is the version policy of LINA. It says what is versioned, how each version is written and which one decides compatibility. The release procedure is in [releases.md](../policy/releases.md), the protocol versions are in [host-protocol.md](host-protocol.md#versions-and-capabilities) and the artifacts are listed in [product-families.md](product-families.md#repository-artifacts-and-versions).

## Product version

- LINA is `0.0.1` alpha and stays so until the owner decides otherwise. A finished milestone does not change the version.
- A release is tagged `v0.0.1-alpha.YYYYMMDD.N`. `YYYYMMDD` is the date of tagging in UTC and `N` counts the releases of that day from 1. Example: `v0.0.1-alpha.20261020.1`.
- The tag is a valid SemVer pre-release and a valid Go module version, and tags sort by date and then by `N`. A tag carries no build metadata (`+<sha>`), because Go ignores it. The commit is recorded in the binary instead.
- Nothing is tagged before the first milestone (M0) is merged.
- LINA Core, the TUI, Node and the protocol files share one tag.

## What a binary reports

`lina version` prints `0.0.1-alpha.YYYYMMDD.N (commit <short sha>, built <date>)`. The build sets these values with `-ldflags`. A build without a tag prints `0.0.1-alpha.dev (commit <sha>)`.

## Other versions

- The protocol has its own `MAJOR.MINOR` per connection, defined in [host-protocol.md](host-protocol.md#versions-and-capabilities). Its major number decides compatibility. The product tag never does.
- The state schema has its own integer. Migrations only go forward. A release that changes it says so in its notes.
- The LINA kit has its own integer in `kit/VERSION`, recorded in the build manifest ([runtime.md](runtime.md#release-manifest)).
- The LINA app versions separately, in its own repository, and declares the range of LINA Core it supports.

## Releases

A release is a tag on `dev` made through [releases.md](../policy/releases.md), with a GitHub Release whose notes are generated from the titles and labels of the merged pull requests. There are no release branches while LINA is alpha, unless a hotfix needs one.

## Open decision

When and how LINA leaves alpha, with a beta tag or `1.0.0` at the first personal-edition release, is for the owner to decide. Until then the version stays `0.0.1` alpha.
