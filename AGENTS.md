# LINA Working Rules

These rules are for the worker reading them: a coding tool, such as Codex or Claude Code, working on this repository. In LINA's design documents, an agent is a persona that the user creates, and a worker is a coding tool.

Follow [MANIFESTO.md](MANIFESTO.md) for product principles. The user's latest explicit instructions take precedence.
Product design contracts live in [docs/design](docs/design/); implement against them, and change a contract in its file through the same [integration](docs/policy/integration.md) as any other change.

- Read the requested scope and relevant files before making changes. Keep changes focused.
- Write branch names, commit messages and merge commit messages in English, regardless of the language used to request the work. Keep quoted source text, logs, and code unchanged.
- Branch from `dev` and open one pull request per issue into `dev`. Verify with `node scripts/foundation.mjs` ([foundation.md](docs/policy/foundation.md)), obtain one bot review, and merge with a merge commit after the pull request's `foundation` check is green; never squash or rebase. `node scripts/integrate.mjs <branch>` remains the owner's local integration path ([integration.md](docs/policy/integration.md)). Only the owner-approved release script advances `main` ([releases.md](docs/policy/releases.md)).
- When the owner delegates a scope (a project or a set of issues) to a worker, the worker may merge its pull requests in that scope once the review and `foundation` conditions pass, unless the owner reserves the merge; outside a delegated scope, merging needs the owner's explicit authorization. Every release and deployment needs the owner's approval.
- Preserve work, branches, and worktrees that belong to other people or workers.
- Verify changes and report what was and was not checked. Do not write tests that pin prose or pass by timing luck.
- Keep secrets, personal conversations, memories, user files, and operational state out of the repository ([SECURITY.md](SECURITY.md)).
- Start every source file with `SPDX-License-Identifier: Apache-2.0` in its comment syntax, after a shebang line if it has one ([LICENSE](LICENSE)).
- Plans and issues live in this repository's GitHub milestones and issues; the roadmap is issue #1.
- An issue names the design sections it implements. A change that alters behavior updates those sections in the same pull request ([work-and-delegation.md](docs/design/work-and-delegation.md#work-definition)).
- Clean up temporary processes and resources created for the task.
