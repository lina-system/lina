# Repository cache reference observations

Read before implementation for #197. The authority is
[C-35](../../../docs/design/go-core.md#node) and
[Remote devices](../../../docs/design/work-and-delegation.md#remote-devices).

- **Seen:** CRW commit `821d6453aacfc9053b17c3b24a134c0ce4077f80`,
  `internal/integrator/integrate.go:52-107`, fetches the remote before resolving
  a base, creates an isolated worktree, refuses an existing target, and removes
  only temporary paths owned by that run. Its worktree lifetime is the
  integration run, not a remote task's pull request.
- **Inferred:** A Node cache should publish a complete clone atomically, isolate
  tasks in owned worktrees, and preserve their files until Core confirms the
  corresponding GitHub merge. Repository identity must name GitHub, never a
  Core filesystem path or a commit transfer channel. Failed or interrupted
  preparation must not invalidate an already usable cache.
- **Unknown:** This reference establishes neither Node transport, task grants,
  in-memory GitHub authentication, nor reconciliation of a vanished Node.
  Those depend on the Node contracts and runtime. No CRW executable, workflow,
  relay, hook or review procedure is used or copied.
