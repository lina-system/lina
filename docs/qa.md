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

`TestHeadless` retains the historical connection-only baseline: it builds the
real binary, creates a synthetic installed identity, starts Core without a model
route in a temporary state root, and drives `lina tui` through a controlling
80-by-24 PTY. It waits for connection state, checks an empty protocol input's
refusal, types `q` and Enter, checks that Core survives TUI quit, and covers EOF
and absent Core. That unconfigured refusal is not a limit on configured Core.
`TestM1FakeCore` scripts replies, refusals and disconnects over an isolated Unix
socket. `TestM1TUIState` exercises duplicate frames and epoch changes at the
receive boundary. These checks do not establish the complete M1 journeys.

### TUI screens and input acceptance (#106)

The parent issue combines the shell (#107), conversation and composer (#108),
and approval and question cards (#109). Run the child issues' package checks
and the existing fake-Core and installed-Core journeys together:

```sh
go test -count=1 ./internal/tui/term/... ./internal/tui/ui/... ./internal/design/... ./cmd/lina/... ./internal/tui/state/...
go test -count=1 ./e2e/... -run '^TestM1TUI(Shell|Recovery|ConversationInstalledCore|ConversationStream)$'
node scripts/foundation.mjs
```

| Parent acceptance | Evidence and remaining work |
| --- | --- |
| Start, connect, refuse, recover and quit through a PTY | `TestM1TUIShell` and `TestM1TUIRecovery`, driven by `e2e/m1/tui-shell.json` and `e2e/m1/tui-recovery.json`. |
| Two fake clients race one approval; exactly one resolves | Depends on #109. Add its approval scenario to this acceptance command when available; conversation input tests do not establish approval resolution. |
| Unchanged state writes zero bytes | `TestUnchangedConnectionWritesNothing` and `TestWorkListsFollowWelcomeCapabilities` check output lengths after rendering the same state twice. |
| Absent Core shows its screen and starts no Core | `TestM1TUIShell` checks the absent state, exit status and absence of newly created state; `TestShellAbsentScreenKeepsHistoryOffScreen` checks the view. |
| Every closed code has a catalog entry by id | `TestCatalogCoversClosedWireCodes` walks schema enums; `TestConnectionCatalogAndReadOnlyStates` checks connection ids in both languages. |
| All child issues are closed | Check #107, #108 and #109 before closing #106. Passing these commands alone does not satisfy this condition. |

The design contracts are [T-04](design/tui.md#approvals-questions-and-sign-in-t-04),
[T-05](design/tui.md#connection-states-t-05),
[T-07](design/tui.md#layout-tiers-and-row-budget-t-07),
[T-10](design/tui.md#input-and-commands-t-10),
[T-12](design/tui.md#home-waiting-for-me-and-progress-t-12),
[T-13](design/tui.md#results-cards-and-the-pager-t-13), and
[T-15](design/tui.md#notifications-t-15).
This acceptance map changes no product behavior. Approval races, stale groups
and replayed approval bells remain #109's acceptance work.

### Synthetic Responses conversation

The shipped conversation path uses an explicit Responses route in Core. Run the
installed Core/TUI journey and the separate fake-Core streaming scenario with:

```sh
go test -count=1 -v ./e2e/... -run '^TestM1TUIConversation(InstalledCore|Stream)$'
```

[`TestM1TUIConversationInstalledCore`](../e2e/m1/tui_conversation_test.go) installs
the binaries in scratch with a fixture service manager, starts a loopback HTTP
provider, and passes these settings to installed Core's otherwise isolated
environment:

| Variable | Synthetic fixture value |
| --- | --- |
| `LINA_RESPONSES_BASE_URL` | The fixture server's runtime URL plus `/v1`; the adapter appends `/responses`. |
| `LINA_RESPONSES_MODEL` | `fixture-model` |
| `LINA_RESPONSES_API_KEY` | `synthetic-key`, a dummy fixture value, never a real credential. |

Core grants `conversation`; the real TUI sends typed input and displays the
synthetic reply from the Responses SSE stream. The fixture emits
`response.output_text.delta`, `response.output_item.done` and
`response.completed`. The test checks parsed terminal cells, durable input
deduplication, and history restoration after restarting Core and reopening the
TUI, with exactly one provider call across the journey. It then restarts Core
without the route and checks that conversation capability is absent.
`TestM1TUIConversationStream` separately uses fake Core to check streaming
completion and steering through the real TUI.

For exploratory conversation QA, reuse the loopback fixture and
`installedConversationHarness` in that test. Supply all three variables
explicitly to Core after clearing the inherited environment; setting them only
on the TUI does not configure Core. Use only a fixture server owned by the QA
session, and close it during cleanup. No real account, sign-in or
`LINA_QA_REAL_MODEL` opt-in is needed. See [the harness guide](../e2e/README.md)
and [the installed scenario](../e2e/m1/tui-conversation.json) for the acceptance
boundary.

### Real-model mode

Acceptance and QA runs use the intended production model rather than a cheaper
substitute, because a weaker model hides the failures that matter. The same
Core and client journeys run against a Responses-compatible endpoint configured
outside the repository:

| Variable | Meaning |
| --- | --- |
| `LINA_QA_REAL_MODEL` | Set to `1` to opt into external model calls. |
| `LINA_RESPONSES_BASE_URL` | The endpoint's API prefix; the adapter appends `/responses`. |
| `LINA_RESPONSES_MODEL` | The exact model to test. There is no default or fallback model. |
| `LINA_RESPONSES_API_KEY` | The endpoint's API key, from the environment. |

```sh
export LINA_QA_REAL_MODEL=1
export LINA_RESPONSES_BASE_URL LINA_RESPONSES_MODEL LINA_RESPONSES_API_KEY
go test -count=1 -v ./e2e/m1 -run 'TestResponsesRoundTrip|TestRealModelHeadlessConversation'
```

`TestResponsesRoundTrip` streams one Core-side request through the configured
adapter. `TestRealModelHeadlessConversation` installs Core, sends a message
through the headless client, requires durable input acknowledgement, streamed
deltas and completed assistant items, and requires the persisted turn to
survive a Core restart under the same conversation id. Without the opt-in or
key both real tests skip instead of failing, so credential-free CI and
foundation stay green. An opted-in run with invalid configuration, an
unreachable endpoint or a broken stream fails. Installation is Linux-only, so
other hosts skip the headless journey.

Keep endpoint values and the key out of shell tracing, logs, reports and
commits. See [the harness guide](../e2e/README.md) for the endpoint rules.

### Connection-only exploration

For connection lifecycle exploration, build from the tested checkout and start
Core with a new state directory and no model route. This example intentionally
does not enable conversation; use the synthetic Responses path above for that
journey. The identity below is a synthetic installation fixture, not a test of
the real installation flow:

```sh
qa_root=$(mktemp -d "${TMPDIR:-/tmp}/lina-qa.XXXXXX")
umask 077
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
interpret a fixed sleep as readiness. Keep socket paths short on macOS. Keep
Core running during the terminal exploration below.

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

Always stop and reap Core after exploration, including on failure:

```sh
kill -TERM "$qa_core_pid"
wait "$qa_core_pid"
```

## M1 journeys

Start conversation QA with the synthetic Responses path above: configured Core
supports streamed replies and durable history through the real TUI. Try the
remaining journeys through their current entry points. Record an unavailable
entry point as a gap with an existing issue reference, rather than claiming a
package test established a user journey.

Current coverage has limits: the installed conversation fixture does not prove
real sign-in or every journey below. Fake-Core scenarios and scripted provider
wires establish only their exercised boundaries. The real-model journeys in
the harness guide run the same Core provider route and the same headless client
path against the configured endpoint; when they skip for a missing opt-in, key
or Linux host, the skip does not establish a client journey. Earlier
connection-only increment notes describe those increments, not the capabilities
of configured Core today. Do not invent flags or use a real account to fill a
coverage gap.

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

The skills portion is covered by [its fixture](../e2e/m1/skills.json) and:

```sh
go test -count=1 -v ./e2e/m1 -run '^TestM1SkillsInstalledCore$'
```

Installed Core offers all four skill tools to a synthetic Responses provider,
commits an instruction-only write, shows its receipt notice in the real TUI,
and returns the catalog handle and body through tools on the next turn. The
same journey refuses an executable helper without a learning notice, undoes
creation, restores the undo notice after restart, and rejects re-adopting the
undone content. This proves the installed wiring with scripted model calls;
real-model tool selection and the separate Hermes import journey are not covered.

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
