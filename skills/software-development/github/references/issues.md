# GitHub Issues Management

## Prerequisites

Use `terminal` for GitHub CLI calls. Check `gh auth status`; read
`references/auth.md` if authentication fails. Specify `-R <org>/<repo>`
when outside the target checkout.

## Procedure

1. Read the full issue with `gh issue view <number> --comments`. For forwarded
   numbers, confirm the title and whether the link is an issue or a PR.
2. Search issues and PRs before creating anything:

```bash
gh issue list --state all --search "<symptom>"
gh pr list --state all --search "<symptom>"
```

3. Write the body using `templates/bug-report.md` or
   `templates/feature-request.md`. LINA issues require these sections:

```markdown
## Summary
What is broken or what capability is needed, and who needs it.

## Done when
- [ ] An observable result that can be verified.

## Context
Reproduction, expected and actual behavior, relevant code or discussions,
dependencies, scope, size and routing rationale.
```

Keep secrets and private account, device and personal details out of public
bodies and comments. For a bug, include enough reproduction and environment
information to distinguish it from intentional design.

4. Read `gh label list -R lina-system/lina` and the target repository's label
   list. Apply the organization set, not invented priority or subsystem labels.
   See SKILL.md for kind, size, dependency and routing labels. Assign a runner
   only from the actual routing decision; `cloud` is paused.

```bash
gh issue create --title "fix: preserve login redirect" --body-file issue-body.md --label bug
gh issue edit <number> --add-label "size/S,needs-routing"
```

5. Read the created issue back and verify its title, body, labels, dependencies
   and milestone. Reuse an existing issue when the work is already tracked.
6. Close as completed only when the acceptance criteria are verified. For a
   duplicate, link the canonical issue and close as not planned. Reopen if the
   reported behavior still fails. Never infer delivery from a worker's report.

## Quick Reference

| Action | Command |
|---|---|
| Read full thread | `gh issue view <number> --comments` |
| Inspect labels | `gh label list -R <org>/<repo>` |
| Apply existing label | `gh issue edit <number> --add-label <label>` |
| Assign | `gh issue edit <number> --add-assignee @me` |
| Reopen | `gh issue reopen <number>` |
| Close duplicate | `gh issue close <number> --reason "not planned"` |

## Verification

Done when the live issue matches the requested work, has finite acceptance
criteria and valid labels, and its state reflects evidence. PRs that fully
resolve it end with `Closes #N` and use merge commits.
