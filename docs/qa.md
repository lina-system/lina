# Real-use QA

Run each session on the merged `dev` head in a new scratch directory. The
repository opts in to devflow's post-merge QA with `DV_QA=1` in `.devflow`.
A QA session explores the product and files small findings; it does not fix code.
Read the current GitHub milestone and [M1 acceptance](design/go-roadmap.md#lina-1)
alongside this guide. Use [the TUI contract](design/tui.md) and
[Core contracts](design/go-core.md) to decide what the person should experience.

## Isolated Core and terminal

Requirements: the Go toolchain pinned by `go.work`, Node.js, and a Linux or macOS
PTY. The existing [headless harness](../e2e/README.md) needs no display server,
provider account or external service. Run its current baseline with:

```sh
go test -count=1 -v ./e2e/...
```

`TestHeadless` builds the real binary, creates a synthetic installed identity,
starts Core in a temporary state root, and drives `lina tui` through a controlling
80-by-24 PTY. It waits for connection state, sends typed protocol input, types
`q` and Enter, checks that Core survives TUI quit, and covers EOF and absent Core.
`TestM1FakeCore` scripts replies, refusals and disconnects over an isolated Unix
socket. `TestM1TUIState` exercises duplicate frames and epoch changes at the
receive boundary. These are a baseline, not proof of the complete M1 journeys.

For exploratory use, build from the tested checkout and start Core with a new
state directory. The identity below is a synthetic installation fixture, not a
replacement for the eventual installation flow:

```sh
qa_root=$(mktemp -d "${TMPDIR:-/tmp}/lina-qa.XXXXXX")
mkdir -p "$qa_root/home" "$qa_root/state/system/core" "$qa_root/out"
go build -mod=readonly -o "$qa_root/lina" ./cmd/lina
printf '{"main_id":"main-qa-synthetic"}\n' > "$qa_root/state/system/core/main.json"
chmod 600 "$qa_root/state/system/core/main.json"
env -i HOME="$qa_root/home" XDG_CONFIG_HOME="$qa_root/home" \
  XDG_STATE_HOME="$qa_root/state" XDG_RUNTIME_DIR="$qa_root/state" \
  PATH= TERM=dumb LANG=C LC_ALL=C \
  "$qa_root/lina" core --state-root "$qa_root/state" \
  --socket "$qa_root/state/client.sock" > "$qa_root/out/core.log" 2>&1 &
qa_core_pid=$!
```

Wait for the socket to exist and Core to remain alive before connecting. Do not
interpret a fixed sleep as readiness. Keep socket paths short on macOS. Always
stop and reap the Core process, including on failure:

```sh
kill -TERM "$qa_core_pid"
wait "$qa_core_pid"
```

To inspect a real TUI without a window, use a private tmux server as the PTY
(or extend the existing harness in scratch). In Bash, with `qa_root` still set:

```sh
qa_tmux="lina-qa-$$"
printf -v qa_tui_command '%q ' env -i HOME="$qa_root/home" \
  XDG_CONFIG_HOME="$qa_root/home" XDG_STATE_HOME="$qa_root/state" \
  XDG_RUNTIME_DIR="$qa_root/state" PATH= TERM=xterm-256color LANG=C LC_ALL=C \
  "$qa_root/lina" tui --socket "$qa_root/state/client.sock"
env -i HOME="$qa_root/home" PATH="$PATH" SHELL=/bin/bash \
  tmux -L "$qa_tmux" -f /dev/null new-session -d -s qa -x 80 -y 24 "$qa_tui_command"
tmux -L "$qa_tmux" capture-pane -p -t qa:0.0 > "$qa_root/out/tui-before.txt"
tmux -L "$qa_tmux" send-keys -t qa:0.0 q Enter
```

Capture after observing the connection state, before sending quit. For more
journeys, send keys and capture the pane at meaningful state transitions; keep
raw transcripts and the rendered screen as evidence. Verify Core is still alive
after TUI quit. Stop only this private tmux server with
`tmux -L "$qa_tmux" kill-server` if it still exists, then stop and reap Core.
Retain scratch evidence until the report is delivered; never remove another
worker's directory or a dirty checkout.

## M1 journeys

Try the following through the real Core and TUI as each entry point lands. Record
an unavailable entry point as a gap with an existing issue reference, rather than
claiming a package test established a user journey. The current connection-only
Core does not grant conversation capability; an input refusal is expected at
this increment. There is no configured end-to-end provider-stub switch yet.
Use the existing fake Core and scripted provider wires for their supported
boundaries, and report that limit. Do not invent flags or use a real account to
make an unfinished journey appear complete.

| Journey | Observe and retain |
| --- | --- |
| Fresh install, no account, then synthetic sign-in | Clear preparation refusal and sign-in offer; no provider call before preparation succeeds; synthetic owner binding survives restart. |
| Conversation and interruption | Short streaming reply, over-long result as a card, steering and cancellation, no tool from a truncated response, one result per call. |
| Background work | A hard-budget turn continues in the background; its result arrives at a turn boundary; the person can keep talking. |
| Remember, recall, forget | Korean synthetic facts recall with evidence, remain scoped to the intended agent, and produce zero hits after forgetting, including after restoring an older scratch generation. |
| Skills and import | Instruction-only skill adoption and undo; executable code is not silently adopted; a synthetic Hermes import reconciles counts and is idempotent. |
| Channels and agents | Synthetic Telegram replay reaches the correct agent once; a non-owner button has no effect; typed agent hand-off stays in the background. Use transport fixtures, never a real chat. |
| Restart and recovery | Planned restart waits for a fake in-flight effect; crash/restart increases epoch without repeating effects; corrupt scratch canon refuses writes. |
| TUI interaction | Waiting-for-me ordering, running count, cards, focus/resize/quit, duplicate frames, reconnect and epoch notice; unchanged state writes no bytes. Assess the rendered UX too. |
| Backup and isolation | Restore synthetic data to a second scratch root; forgotten data stays absent; unavailable sandbox disables effects with a cause; planted tool-output instructions cannot approve an action. |

Use deterministic local provider responses and synthetic identities. Clear the
product environment, including inherited provider credentials, user config and
display settings. Never touch production databases, deployed services, real
provider/API accounts, personal conversations, the owner's files or existing
Core instances. Do not run real sign-in or contact Telegram. A cheap provider is
still a real account and is outside this automated QA authorization.

## Findings and completion

Search open issues by title and keywords before every finding. Comment on a
matching issue; otherwise file one issue per small change with `qa` and `bug` or
`feature`, steps, expected/actual behavior, command output or render path, and
the tested head SHA. Put it in the milestone of the tested code, not a future
milestone. Link existing planned work for features not yet connected.

Under devflow, use `dv qa search JOB 'keywords'` and `dv qa finding JOB FILE`.
The finding file contains `title`, `kind`, `steps`, `expected`, `actual` and
`evidence` strings; use numeric `match` for a matching open issue. The runner
gets the milestone from the merged PR or its closing issues, so preserve that
association on development PRs. If it is missing or ambiguous, report the block.

Write a short report of journeys tried, outcomes, issue/comment links, evidence
paths and untested limits. Run `dv done JOB` to post the one summary on the merged
PR. Keep private paths, credentials and personal data out of published evidence.
The manager prioritizes open `qa` findings in the current milestone. That
milestone stays unfinished and no next-milestone work starts until all its open
`qa` findings are closed and pending/running QA has finished.
