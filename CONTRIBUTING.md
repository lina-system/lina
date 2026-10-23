# Contributing

LINA is licensed under the Apache License, Version 2.0 ([LICENSE](LICENSE), [NOTICE](NOTICE)). A contribution you submit is licensed under the same terms, as section 5 of the license states (inbound equals outbound).

## Every change

- Every source file starts with `SPDX-License-Identifier: Apache-2.0` in its comment syntax.
- Dependencies follow the license rules of [dependencies.md](docs/policy/dependencies.md).
- Design documents and the review and fix history of a change are kept. `dev` is never rewritten ([integration](docs/policy/integration.md)).

## Verification and post-merge QA

Run `node scripts/foundation.mjs` locally before opening a pull request into
`dev`. The PR's required `foundation` check selects documentation, automation,
dependencies, build/analysis and unit checks for the change. E2e is excluded by
default. Add the `e2e` label to opt in to the reached acceptance scenarios, for
example when changing the harness or installation and boot paths. Adding or
removing that label reselects the PR's checks.

Every push to `dev` and the nightly CI run execute full foundation verification,
including every e2e scenario. Failed tests open or update one `qa` issue per
test in the current milestone; a passing observation on a later head closes
it. Post-merge QA findings do not block unrelated PRs. The repository variable
`QA_MILESTONE` names the current milestone number. See the
[foundation policy](docs/policy/foundation.md) for evidence and failure handling.
