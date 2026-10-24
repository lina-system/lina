# Worker evaluation fixture

A synthetic Go repository used only by the worker evaluation defined in
`docs/research/worker-protocols.md` and recorded in
`evidence/worker-evaluation.md`. It is seeded at one pinned commit and copied
into a fresh worktree for each run. The task packet is in `../task.md`.

`main_test.go` writes the path in `WEVAL_TRACE` when the package's tests run, so
the harness can observe that the worker executed the suite without adding a
file to the result. Leave it unchanged.
