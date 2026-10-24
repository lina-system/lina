# Worker evaluation: one task through every worker connection

Result of the evaluation defined in
[worker-protocols.md](../docs/research/worker-protocols.md#evaluation-defined-before-building)
and required by [#760](https://github.com/thisisjun786/lina/issues/760). It ran
the same task through each worker connection on 2026-10-23 and scores each
dimension separately. It records run ids, codes, counts and versions only.

## Result

- **[CC] keeps ACP.** `claude-agent-acp` passed start, progress, permission
  routing, cancel, resume, done, outcome and trajectory on the same task, and so
  did Cursor and Grok through the same client. The [CC] headless adapter passed
  start, progress and done detection only: with no live approval channel the
  worker's own default mode asked for the edit and the command, both were
  denied, and the turn ended as `permission_denials` with no commit.
- **Codex stays on the app-server**, as the research note concluded. It passed
  every dimension, including recovery of the same thread after a kill.
- **Grok asks nothing.** Its own configuration approved the edit and the
  command, so the permission dimension is not applicable for it and is recorded
  with that mode.
- **Gemini CLI is not observed.** It is not installed on this host and has no
  account, so only the earlier handshake measurement covers it.
- **Antigravity completes nothing past start without a person.** Its headless
  mode has no live approval channel, so it waits on its first write; the run
  spent its whole budget and produced no completion event.

## Conditions

Fixed before the runs, as the research note requires.

| Item | Value |
| --- | --- |
| Host | Linux 7.0.0-38-generic, x86-64, 24 CPUs |
| Toolchains | Go 1.27.3, Node.js 24.20.0, Git 2.53.0 |
| Fixture | [testdata/worker-eval](../testdata/worker-eval/README.md), seed commit `1ee46b1db3246eef42d155bccdbe45bca411f161` from `setup.sh seed` |
| Task | [task.md](../testdata/worker-eval/task.md): add `Multiply` and its test, run the suite, commit once, no push |
| Cancel task | [task-cancel.md](../testdata/worker-eval/task-cancel.md): run a two-minute tagged test, then the suite |
| Isolation | A fresh worktree per run from the seed; the person's own install and sign-in; nothing imposed on the worker |
| Budgets | Fifteen minutes per run; at most three runs per candidate |
| Stop rules | Two start failures in a row mark a candidate blocked; a candidate without an install or account is not observed |

The earlier runs used a seed that can no longer be reproduced. The seed commit
above is the one `setup.sh seed` creates from the published fixture.

Each run drives one adapter directly, in one worktree, and answers the worker's
requests itself: a permission request is answered `allow_once`, and a work-tool
call is answered with a success. No scheduling, no Core, no pull request. The
adapter under test is the one Core uses, and the versions below are the ones
each adapter recorded.

| Candidate | Connection | Worker version | Adapter version |
| --- | --- | --- | --- |
| Codex | `codex app-server` | codex-cli 0.163.0 | LINA adapter, generated types |
| [CC] | `claude-agent-acp` | 2.1.309 | 0.94.0 |
| [CC] | `claude --print` | 2.1.309 | LINA adapter |
| Cursor | `cursor-agent acp` | 2026.10.22-27dcf5f | shared ACP adapter |
| Gemini CLI | `gemini --acp` | not installed | not run |
| Grok | `grok agent stdio` | grok 1.0.67 | shared ACP adapter |
| Antigravity | `agy` headless | 1.4.6 | LINA adapter |

## Per-dimension results

`pass` means the dimension's condition held in the observed run. `fail` means
the run reached the dimension and did not satisfy it. `n/a` means the dimension
does not apply to that connection or that run, with the reason recorded. The
event count after `pass` is that run's progress events.

| Candidate | Start | Progress | Permission | Cancel | Resume | Done | Outcome | Trajectory |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Codex | pass | pass 161 | pass 3 | pass 6 ms | pass | pass | pass | pass |
| [CC] ACP | pass | pass 37 | pass 2 | pass 15 ms | pass | pass | pass | pass on the task run; fail on the resume run |
| [CC] headless | pass | pass 5 | fail, no channel | n/a, same failure | n/a | pass, typed failure | fail | fail |
| Cursor | pass | pass 187 | pass 2 | pass 5 ms | pass | pass | pass | pass |
| Gemini CLI | not observed | not observed | not observed | not observed | not observed | not observed | not observed | not observed |
| Grok | pass | pass 258 | n/a, 0 asked | pass 7 ms | pass | pass | pass | pass on the task run; fail on the resume run |
| Antigravity | pass | pass 10 | fail, no channel | fail | n/a | fail | fail | fail |

## Runs

| Id | Result |
| --- | --- |
| `codex/main` | done after one commit whose suite trace precedes the commit; the suite passes at the result commit |
| `codex/cancel` | `interrupted` 6 ms after the cancel, no tool call after it, no commit |
| `codex/resume` | the kill after the commit reported `connection`/`connection_lost`; the same thread resumed, its killed turn read back as interrupted, the next turn finished `done`; one commit in total, trace before commit |
| `claude-acp/main` | done after one commit whose suite trace precedes it; suite passes |
| `claude-acp/cancel` | `interrupted` with stop reason `cancelled` 15 ms after the cancel, no commit, no tool call after it |
| `claude-acp/resume` | the kill reported `connection`/`prompt_failed`; `session/load` restored the same session id, the next turn finished `done`; one commit, but the suite trace follows the commit |
| `claude/main` | `failed`/`worker`/`permission_denials` after 5 progress events; no commit |
| `claude/cancel` | the same `permission_denials` failure before any long step; the cancel never applied |
| `cursor/main` | done after one commit whose suite trace precedes it; suite passes |
| `cursor/cancel` | `interrupted` with stop reason `cancelled` 5 ms after the cancel, no commit |
| `cursor/resume` | the kill reported `connection`/`prompt_failed`; the same session was restored, the next turn finished `done`, one commit, trace before commit |
| `grok/main` | done after one commit whose suite trace precedes it; suite passes |
| `grok/cancel` | `interrupted` with stop reason `cancelled` 7 ms after the cancel, no commit |
| `grok/resume` | the kill reported `connection`/`prompt_failed`; the same session was restored, the next turn finished `done`, one commit, trace after commit |
| `antigravity/main` | start and 10 progress events, then the worker waited on its first write with no approval channel; no completion event within the fifteen-minute budget, no commit |
| `antigravity/cancel` | the process stop ended the turn as `failed`/`worker`/`denied_actions`; not interrupted, no long step reached, no commit |

## What each run showed

- **Start.** Every installed candidate opened a session in the requested
  worktree, and each adapter recorded the version it found. The ACP adapter also
  recorded `agentInfo.version`: 0.94.0 for [CC], 1.0.67 for Grok, and none for
  Cursor, whose `initialize` omits it.
- **Streamed progress.** Every candidate that ran delivered progress before the
  turn ended. Cursor sent the most for one task (187 events) and [CC] through
  ACP the fewest (37).
- **Permission routed.** Codex asked three times (a command, a file change and a
  command), [CC] and Cursor twice, and every request reached the caller and its
  answer returned. Grok asked nothing: its own configuration approved the edit
  and the command, so this dimension does not apply to it. Antigravity and [CC]
  headless have no live approval channel; they report denials instead.
- **Cancel.** Codex, [CC] through ACP, Cursor and Grok all ended as interrupted
  within 78 ms, with no tool call afterwards. Antigravity stops only by killing
  the process, and its adapter reported the stopped turn as a worker failure,
  not as interrupted.
- **Resume after kill.** The four connections that were killed mid-turn
  reported the lost process as a connection failure, never as done. Codex
  resumed its thread and read the killed turn back as interrupted. [CC], Cursor
  and Grok restored the same session id and finished the task on the next turn
  with a single commit, so no committed effect repeated. [CC] headless and
  Antigravity cannot resume.
- **Done detection.** A typed done arrived only where a commit existed. The
  [CC] headless failure was typed `permission_denials` and never claimed done,
  which satisfies this dimension; Antigravity produced no completion event at
  all, which does not.
- **Outcome.** The suite passes at the result commit of every run that
  committed, checked by running it in that worktree.
- **Trajectory.** On every task run the suite's trace preceded the commit, for
  Codex, [CC], Cursor and Grok. On the resume runs the order varied: Codex and
  Cursor ran the suite before committing, while [CC] and Grok committed first
  and ran it afterwards. No run wrote outside its worktree.

## Decision

[CC] keeps the ACP connection (`claude-agent-acp` with the person's own
`claude`). The headless adapter stays available only as an explicit selection.

It satisfies every dimension on this task, including the two that matter most
for a hand-off: it routes the worker's approvals to the caller, and after a kill
it restores the same session and finishes without repeating a committed effect.
The headless adapter has neither, and on the same task it could not finish,
because the worker asked and nothing could answer. Nothing else in the
comparison beats ACP for [CC] on any dimension, and the two candidates that
share the same client behave the same way.

Confidence: high for this task on this host, from runs that all completed
inside their budget. This is not a ranking of models or of code quality, and a
worker that asks for no approval or that cannot resume is untouched by it.

## Limits

- One host, one person's setup and model routing, and one synthetic task per
  connection. The task needs one file write and one command, so a worker whose
  own mode asks nothing shows no permission result.
- Gemini CLI was not installed, so its row repeats the research note's handshake
  measurement and adds nothing.
- Antigravity never reached a long step: its headless mode waited on a write it
  could not be approved for, so cancel and the dimensions after it were not
  exercised on a running turn.
- This is not test, review, CI or merge evidence. It does not establish that a
  worker's pull request is green, and it says nothing about the Core dispatch
  path, which the acceptance harness covers separately.
