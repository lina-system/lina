# Worker evaluation fixture

Synthetic inputs for the worker evaluation recorded in
[`evidence/worker-evaluation.md`](../../evidence/worker-evaluation.md), which
answers the evaluation defined in
[`docs/research/worker-protocols.md`](../../docs/research/worker-protocols.md#evaluation-defined-before-building).
Nothing here is product code and no test imports it.

- `fixture/` is the pinned synthetic repository. `go test -count=1 ./...`
  passes at the seed commit; `main_test.go` writes the path in `WEVAL_TRACE`
  when the suite runs, so an observer can see that the worker executed it
  without adding a file to the result. `long_test.go` is a two-minute step
  behind the `long` build tag, so only the cancel run pays for it.
- `task.md` is the task packet: add `Multiply` and its test, run the suite and
  commit once. It needs one file write and one command, so a worker in its own
  default permission mode is likely to ask.
- `task-cancel.md` is the cancel packet: run the long-tagged test, then the
  suite. Nothing is written or committed.
- `setup.sh seed <dir>` copies the fixture into a fresh Git repository and
  prints the seed commit. `setup.sh run <seed> <worktree> <branch>` adds one
  run's worktree from that commit.

Each run works in its own worktree and is driven directly through one worker
adapter, not through Core, so the measurement is of the connection rather than
of scheduling. The driver, like the probe behind the research note, stays
outside the repository; the record keeps only run ids, codes, counts and
versions.
