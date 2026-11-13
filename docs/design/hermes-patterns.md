# Hermes patterns

Intake: the source is the operators' private lesson log. Once a week, only workarounds or fixes that have run are moved here.
Lessons are written in English by role, with no dates, agent names or machine names. Unrun lessons go under Open.

Each pattern has four fields: Habit, Worked, Hurt and LINA form. LINA form names a path in this repository, an issue here (`#n`), or says "not built". Roles are system, scheduling, development lead, knowledge, life, creative, investing and legal. Numbers are values seen in operations, not LINA measurements. Pattern numbers are kept from earlier notes and intake threads. New patterns take the next free number.

## Patterns

### P1. Agent personas and authority

- **Habit.** Each agent has a role, a voice and limits on what it may do.
- **Worked.** One domain per agent with a named hand-off. Reversible work is done and reported. Credentials, exposure, deletion and spending are asked.
- **Hurt.** Authority lived in persona text. The hand-off section repeated in every role file, and role files rode every turn. Approvals could be switched off, so a limit held only where a wrapper enforced it. Personas churned and had no revisions.
- **LINA form.** One profile per agent, each with its own persona, memory and skills (`hermes_cli/profiles.py`). Approvals in `tools/approval.py`. Advise-only and draft-only limits for the investing and legal roles: not built.

### P2. Agents ask each other

- **Habit.** Agents hand work to each other, and a busy recipient queues the request instead of losing it.
- **Worked.** Queueing instead of failing. A visible sender. A time limit that ends the retry. One report to the person from the agent that finished.
- **Hurt.** Sending was a blocking agent turn. Busy was detected by matching strings. With no idempotency key, a timed-out call that did run was sent again.
- **LINA form.** The Hermes relay and mailbox (`tools/bot_relay.py`, `gateway/session_bot_mailbox.py`). A typed hand-off with a delivery id for deduplication: not built.

### P3. One gateway for many bots

- **Habit.** One process serves every bot, each with its own channel, and a failure stays in its channel.
- **Worked.** Health per adapter. A route resolved once at ingress, and an unresolved one dropped. Pairing by a one-time code, not first come. Buttons that resolve before they render.
- **Hurt.** Profile isolation was ambient and held in place by lint rules. The Telegram library advanced its offset before handlers finished. An offline remote machine once delayed every adapter's startup.
- **LINA form.** The multiplexing gateway (`gateway/run.py`) with routes per profile (`gateway/profile_routing.py`), secrets per profile (`agent/secret_scope.py`) and code pairing (`gateway/pairing.py`). Telegram comes first (#1).

### P4. Idle-only restarts

- **Habit.** The shared process restarts only when no agent is mid-work, and a restart is verified before it is reported.
- **Worked.** Exit code 75 for a planned restart. An idle proof in which unknown counts as busy. A durable "restart owed" record before the first irreversible step. Confirming a restart by a new process id after the request, not by a marker for one reason. That check showed two pending restarts merged into one.
- **Hurt.** Busy was counted in separate ledgers per kind of work, and each omission shipped as a bug. Idle defined as chat silence ignored work in flight. Restart requests shared one service name, so a second request replaced the first, and the first one's watcher reported "no restart" a day later. In one test, waiting for idle took 53 and 20 minutes because chat kept breaking the quiet window.
- **LINA form.** `gateway/restart.py` (exit 75), `gateway/drain_control.py` and `gateway/run_idle_gates.py`, which fail toward busy. Idle decided from every effect in flight: not built.

### P5. Capped fact memory

- **Habit.** A short store of lasting facts rides every turn. Procedures and task progress live elsewhere.
- **Worked.** The routing rule: facts to memory, procedures to skills, progress to history. Declarative entries, because imperative notes are re-read as commands. Atomic batch edits.
- **Hurt.** Free text needed substring addressing, so ambiguity was a permanent error class. The prompt snapshot hid writes until a session boundary.
- **LINA form.** `tools/memory_tool.py` as in Hermes. Memory items in one store with one receive path: #20, #3.

### P6. User modelling

- **Habit.** A model of the person is built from real conversation and offered to every agent.
- **Worked.** Dated conclusions with their sources. A short profile card. Reinforcement instead of growth.
- **Hurt.** Conclusions were kept per observer, so one fact was stored once per agent. Machine-written text, such as scheduled deliveries and verification turns, could reach the person's model. The card had no versions.
- **LINA form.** The Honcho server from `services/honcho/` (`docs/honcho.md`) and its adapter in `plugins/memory/honcho/`. One memory layer over conversations, Honcho and external messages: #2. Keeping machine-written text out: not built.

### P7. Lossless context

- **Habit.** Old context is summarized, raw text stays recoverable, and large outputs live outside the window.
- **Worked.** Losslessness as the rule. Atomic, coverage-checked publication of summaries. Exact expansion of any summary to its source text.
- **Hurt.** Host and plugin each kept a transcript. Compaction blocked the turn and broke the prompt cache. Korean search fell back to a scan. Secrets stayed in the raw store.
- **LINA form.** `agent/context_compressor.py` today. LCM-X as the core context engine: #15, #16.

### P8. Skills learned while working

- **Habit.** A procedure learned in work is saved as a skill and loaded when it applies.
- **Worked.** A one-line index in the prompt, with the body loaded on demand. Archive instead of delete. An audit ledger with rollback.
- **Hurt.** Review cost grew with context size. Skills grew into runbooks with incident notes. Sharing between agents was by hand copy. A growth loop without independent evidence promoted moods and injected suggestions.
- **LINA form.** `tools/skill_manager_tool.py`, `agent/background_review.py`, `agent/curator.py` and `tools/skill_ledger.py`, kept through the prune by #67.

### P9. Events over polling

- **Habit.** A completion event wakes the party that owns the work.
- **Worked.** Deterministic ids with the generation in them. A cursor moved after hand-off. A reconcile pass behind the events. Before judging a "finished" notice, checking the run's state and its commit against the reported result. A pause accepted only when its reason names what ends it, such as a run id. A paused card spends no model turn and resumes in the same session on the run's end notice. In the first day after the move, 14 runs ended and every card got its end comment. A card closes only when every run it started has a verdict.
- **Hurt.** Wakes were at most once, so a crashed worker was never retried. A "finished" notice arrived just after a new round started and carried the previous round's result. Goal cards could not pause, so waits ran as shell loops. While the old tool stayed configured, one card retried its wake for more than four hours. A run's end notice woke the card before the drive parked it, which cost an extra session each time.
- **LINA form.** Process completion notices (`tools/process_registry_notifications.py`) and board watchers (`gateway/kanban_watchers.py`). Item state recomputed on write: #36. A finish notice that carries its round and a result fingerprint: not built. Parks and wakes run by the hub (`hermes_cli/worker/goal_drive.py`, #461). Closing refused while a run has no verdict (`hermes_cli/kanban_db_verdicts.py`, #295).

### P10. Delegated coding

- **Habit.** Coding goes to external coding tools, the development lead checks the result, and merges follow rules the repository enforces.
- **Worked.** Verification by the coordinator, never the worker's own report. A merge bound to the head that was read. Plain delegation over a heavy harness. Merging the worker branch as it is. Merging only on green. A change that turns a check on also makes it pass. Waiting for merges and checks handed to small background units that wake the card.
- **Hurt.** The review rule lived in merge scripts. One script lacked it, and two pull requests merged with open P1 findings. A worker branch re-applied as patches lost its subtree merge: the import landed with no files, every check stayed green, and the issue closed. Merges went on while the base branch was red. A coding worker asked to wait for a merge was closed as stalled with nothing to show.
- **LINA form.** Runners behind `lina worker`: #35. Required checks: `docs/ci.md`. Review and linked issue as required checks: #61. One merge path: #63. Comparing the pull request diff with the worker branch diff: not built.

### P11. Confirmation for sending, paying, publishing, deleting

- **Habit.** Some acts always wait for the person.
- **Worked.** Consent wording. Timeouts that deny. Grouping by scope. Buttons that resolve before they render.
- **Hurt.** Pattern gates are incomplete by construction. A typed approval in the agent's own channel deadlocked. Approval cards lived in memory and expired on restart.
- **LINA form.** Hermes approvals with hard floors (`tools/approval.py`, `tools/approval_floors.py`). A session bypass still exists (`tools/approval_yolo.py`). Consent tiers on every path: not built.

### P12. Secret hygiene

- **Habit.** Secrets never appear in output, a command line or a log, and masking never hides what is needed.
- **Worked.** One egress chokepoint. One environment builder that drops provider credentials from child processes. Masking content only, never paths, ids or digests. Tests with realistic path lengths, run on the person's operating system before merge. Undo from a separate private copy that is never shown.
- **Hurt.** Redaction is a heuristic denylist. A broad pattern also masked real skill paths longer than its threshold, and CI with short paths passed. A masked snapshot planned as the undo source could restore nothing.
- **LINA form.** `agent/redact.py` and `tools/environments/local_env_policy.py`. macOS tests on pushes to `dev`: `docs/ci.md`. Masking at admission that leaves structural fields clear: not built.

### P13. Untrusted text is data

- **Habit.** Pages, logs, documents and other agents' output never instruct.
- **Worked.** Retrieved content wrapped in a fixed preamble with its delimiters defanged. No fast path for text that already looks wrapped.
- **Hurt.** Coverage went by tool name, so output that passed through a shell was not framed.
- **LINA form.** `tools/threat_patterns.py`, shared by `agent/prompt_builder.py`, `tools/memory_tool.py` and tool results. Trust decided by the entry path: not built.

### P14. Short reports that separate verified from guessed

- **Habit.** Reports are short, say what was checked, and never mix fact and guess in one sentence.
- **Worked.** Honest status words: "tried" is not "done", and unknown is a legal answer. Re-measuring each serious finding of a read-only audit with one command before it entered the report: six of the workers' main conclusions changed.
- **Hurt.** All of it was prose, and nothing checked it. Logs named green held a failing exit code.
- **LINA form.** `agent/verification_evidence.py` records what a coding turn proved. A checked outcome line: not built.

### P15. Work across several machines

- **Habit.** Work runs on several devices as one.
- **Worked.** A map from tool name to remote command, with unmapped names refused. No remote machine gates startup. Heavy runs (full test suites, builds, repeated runs) handed to build hosts through the worker queue, which counts them against per-host caps set from measured daemon and runner memory.
- **Hurt.** Reachability was the only precondition. Agent sessions ran tests, builds and SSH loops straight on the shared host, 175 runs and 663 SSH calls in one day, while the build hosts' queues stood empty. The shared host, which also serves every agent session and the backups, once reached a load of 115. One unthrottled scan of storage snapshots pushed the storage host's load to 260, dropped remote shells and ended in an out-of-memory kill.
- **LINA form.** LINA Helper holds the Mac permissions (`helper/macos/`, #45). Registered devices with grants: not built. Bids pass a node that is short of memory or keeps failing quickly (`hermes_cli/worker/bidding.py`, #456).

### P16. Shared file rules

- **Habit.** Originals stay where the person keeps them. One system of record per kind of information. Duplicates are reported, never deleted.
- **Worked.** One shared rule file that every agent reads.
- **Hurt.** The rules were prose, and only some agents kept them.
- **LINA form.** The Desk (`docs/desk.md`): one shared folder, `desk:` references, nothing deleted without approval, and a write lock across processes.

### P17. Invented work to fill idle capacity

- **Habit.** Keep workers busy without making up work.
- **Worked.** Turning the generator off. Filling a slot only with work tied to a goal, leaving it empty with the reason, and naming what blocks the planned work.
- **Hurt.** A script made one chore per test package: 62 in one day, 46 merged, and the 13 to 16 minute test time did not move. Chores counted as planned work, so the release looked further along and the real blocker stayed hidden. Commit counts were read as progress.
- **LINA form.** A work ledger of goals the person handed over: #36. There is no fill loop.

### P18. Work sessions killed by a restart

- **Habit.** Long-running work outlives a restart of the shared process.
- **Worked.** Starting work sessions outside the gateway's process tree, one at a time per name. A ledger check that marks a goal stalled when its session is gone.
- **Hurt.** Sessions started as gateway children died with it, and no signal followed. Silence was read as progress. The restart checked chat activity, not work in flight.
- **LINA form.** Stall notices for pending input (`gateway/session_stall.py`). Background runs that resume after a restart and tell the delegating agent when they end: not built.

### P19. A condition with no one to meet it

- **Habit.** A deferred step has an owner and a check that it happened.
- **Worked.** Attaching an executor in the same turn as the deferral. A checker that tells the owning agent when a wait has no running work. A weekly direction check that found dropped review findings and filed them.
- **Hurt.** "Add the host once X lands" was recorded, and X had no worker. The fastest host sat idle while others were overloaded. Review findings left as "follow-up by hand" were never filed, and six vanished.
- **LINA form.** Deadlines on goals and reminders that name the next command: #36. A check on every step left to be done by hand: not built.

### P20. A host order that only broke ties

- **Habit.** Work goes to hosts in the order the person set.
- **Worked.** The fixed order as the first rule, with overload only skipping a host. Testing that a setting changes real assignments before saying it is applied.
- **Hurt.** The order was read only when scores tied, so the host with the most slots took nearly everything, even at 87°C. A preference stored as a tie-break looks configured and changes nothing.
- **LINA form.** not built.

### P21. A sign-in link that cannot leave the terminal

- **Habit.** A one-time sign-in URL reaches the person's browser intact.
- **Worked.** A modifier-drag workaround, a pasted callback, a tunnel command in the guide, and testing sign-in from the person's own terminals.
- **Hurt.** The terminal UI captured the mouse. The URL was hard-wrapped, so it could not be clicked or copied whole. Over SSH the redirect could not return. The first run had one path, never tried from the person's terminal.
- **LINA form.** not built.

### P22. An incoming call no one saw

- **Habit.** Answer only the calls the person allows, every time.
- **Worked.** Recording the ring from the system log, so the miss was proven. Finding the banner through a lower-level window query and checking the full allowed address before accepting. Rejecting the system auto-answer, which cannot be limited to the allowlist.
- **Hurt.** Screen scraping looked only at ordinary windows. "Ready" was reported without one real call. The allowlist held an e-mail address while calls arrive from a phone number.
- **LINA form.** Device permissions through LINA Helper (`tools/lina_helper/`, #45). Answering calls: not built.

### P23. A home path resolved on the wrong machine

- **Habit.** A command handed to the person runs on the host it names.
- **Worked.** Absolute remote paths, and running the exact line from a machine like the person's before sending it.
- **Hurt.** The local shell expanded `~` before SSH ran, in two guides. Guides were tested on the server.
- **LINA form.** not built.

### P24. A screen that folded the facts too

- **Habit.** Keep the default screen short without hiding what the person needs to judge.
- **Worked.** Folding records (logs, measurements, pull request lists) and leaving open the facts for the next decision. Rows by area with aligned columns. One colour per state with a legend. A situation band at the top that hides when empty.
- **Hurt.** A brevity rule for wording was applied to information. Uneven card grids, the key line at the bottom in grey, one colour for two states, and ages with no label.
- **LINA form.** The client API the board reads: #39. Screen rules: not built.

### P25. Screen QA by one reviewer

- **Habit.** Screen QA catches what the person will notice.
- **Worked.** Three reviewers who do not see each other's results: the person's eye, a first-time user and a clutter reader. Fix what two agree on or what one shows broken. Open what is judged, fold records.
- **Hurt.** One reviewer's taste became the design.
- **LINA form.** not built.

### P26. An instruction that outlived its issues

- **Habit.** What the person asked for is done when its check passes, not when its issues close.
- **Worked.** A ledger of the person's instructions: the person's words as basis, a done check, a stall raised after two hours, and agents' own promises in the same ledger.
- **Hurt.** Issues were the unit of intent, and a session's end was read as done. Left over: a basis time made up instead of copied, a vague time turned into noon, and an agent closing its own goal with its own note.
- **LINA form.** The work ledger in the core: #36. Basis times copied only from the source: not built.

### P27. A tool written into the rules but never used

- **Habit.** A new capability counts only after it has run on real work.
- **Worked.** Running each worker kind once end to end on a real job. Asking each agent why it had not used the tool. A notice to the delegating agent when the worker ends.
- **Hurt.** Rules were taken as adoption.
- **LINA form.** `scripts/agent_acceptance.py` drives each agent through real surfaces, and a check with no configuration fails. Non-zero use counts after the move: #67.

### P28. Changing text at the front of the cached prompt

- **Habit.** Keep what changes between runs out of the cached prefix.
- **Worked.** Moving per-run skill pointers from the end of the system message to the first line of the turn message. First-request cache reads went back to 99 to 100 percent. Reading cache reads and writes per request from the provider's usage counts. Measuring that the cache lived an hour, so 412 of 421 calls after 5 to 60 minute gaps still hit: the rewrites came from prefix changes, not from gaps.
- **Hurt.** 42 of 45 relaunches with a changed set missed the cache, about a quarter of that day's cache-write cost. It was found from cost, a day later. Editing the shared persona rebuilt every live chat session's prompt on its next turn, 300,000 to 650,000 tokens each. A change of account behind the proxy emptied the cache, and every conversation wrote it once more.
- **LINA form.** `agent/prompt_caching.py` and `agent/prompt_cache_boundary.py`. A fixed context order with a prefix test: #21. Measured against the baseline: #25. An alarm on a drop in first-request cache reads: not built.

### P29. A display mask used where the exact value is needed

Merged into P12.

### P30. A shared tool that took one agent's home as everyone's

- **Habit.** A shared tool sees every agent, whichever agent runs it.
- **Worked.** The tool reads the shared root from its own setting and ignores the per-agent value. A configured agent whose store is missing fails the run by name. A temporary home is given to one command only (`env` or a subshell), never exported. LINA keeps a calling agent's home as the owner of the run, not as its root.
- **Hurt.** Run from one agent's shell, the collector took that agent's home as the root, found none of the other seven, and exited successfully. An exported test home stayed in a terminal, and later hand-offs went out under another agent's name. The same mistake came back a week later and an acceptance did not go through. A test run on the shared host with no home of its own installed its runtime into the live agent home, and the live agent re-synced its dependencies on its next start. On macOS an end-to-end test escaped its isolation and left 22 jobs in the account's service manager.
- **LINA form.** `get_hermes_home()` in `hermes_constants.py` and `_get_profiles_root()` in `hermes_cli/profiles.py`. LINA's own home: #30. Failing by name on a missing agent: not built. The caller's home kept as `LINA_CALLER_HERMES_HOME` (`docs/switch-callers.md`, #434). End-to-end children kept out of the account's service manager: #372.

### P31. A provider outage that stopped scheduled duties

- **Habit.** Reports and releases the person relies on still happen when the model provider is down.
- **Worked.** A fallback to a second provider, which fired once. The person's decision: fallback is the person's switch and off by default, and model-free scheduled duties run either way.
- **Hurt.** Every account of the main provider returned rate limits for about seven hours. Every wake of the management session failed, including the morning report and the release of a hold on the build hosts. The failures repeated silently.
- **LINA form.** `fallback_providers`, empty by default (`hermes_cli/config_defaults.py`, `agent/agent_init_fallback.py`). Model calls go through magpie (#7, #32). Cron holds a job while a provider window is closed (`cron/quota_hold.py`) and runs script jobs with no agent (`cron/jobs.py`). Person reports on a model-free path: not built.

### P32. A finished run whose session stayed open

- **Habit.** A run counts as finished when its work is done, whether or not its session says so.
- **Worked.** Ending a run whose session record has not moved for a grace period: done when its report is in, stalled when it is not. The grace came from measurement. Across 14,947 assistant turns on three build hosts, the longest gap after a tool result was 304 seconds, so the grace is 600. Before that fix, a worker that had written its results was closed five minutes after it went quiet.
- **Hurt.** The coding engine kept sessions "running, streaming" for hours after the commit and the report, and the settle step read that as work. One run sat open for six hours after its report, and its next round did the same. A run whose engine had finished but left a helper process alive ended only at the 12-hour hard timeout.
- **LINA form.** `hermes_cli/worker/runners/omo_settle.py` (`frozen_done`) with `stall_grace_s` in `hermes_cli/worker/device.py`: #404.

### P33. Switching the model proxy under load

- **Habit.** A model path moves one consumer at a time, under real concurrency, with a way back.
- **Worked.** Testing concurrency before the move. The new proxy starts one process per subscription request, and its memory cap allowed six at once. With the cap raised, 60 concurrent conversations all succeeded. Restarting the agents' gateway with a seven-minute check and automatic rollback: every chat channel answered and 50 proxied calls had no error. In the same long-input test, the second request read 99 percent from cache, against none through the old proxy.
- **Hurt.** At the old cap, eight concurrent requests waited 52 seconds. The model alias table is shared with the build hosts. Changing it on the hub first broke their coding sessions, and it was reverted. The proxy's own connection command wrote a placeholder where the key belongs, so the hub refused the coding tools with 401 and the move to coding tools was put on hold. In one catalog shape the same command replaced the key reference with its literal name.
- **LINA form.** One node at a time, each node on its own magpie, one model name (`<provider>/<id>`) with no name table, and a bid that reads the node's own magpie answer (`/v1/models` 200 with at least one model): `docs/worker-proxy-switch.md`, `hermes_cli/worker/proxy.py`, #585, #586, #590.

### P34. A deploy checked on disk instead of in the running process

- **Habit.** A change is live when the running process uses it.
- **Worked.** Restarting the services and rewriting the launch shims after each pin, then reading the running process's environment or mapped files for the pinned version. Pinning idle nodes first, and busy nodes once their runs ended.
- **Hurt.** Services and shims pinned the code path by version, so moving the "current" link alone left the old code running. A launch flag reached a service only through a restart, and a missed restart left it out. A long-running session kept the plugin code it started with after the checkout changed.
- **LINA form.** The services restart in `hermes_cli/lina_services.py`. A check that compares the running version with the pinned one: not built.

## Open

- Agents rewrite skills, memory and conversation memory hundreds of times a day with no reviewable history. A daily card of what was learned, with ok, fix and drop, is being built.
- A hold or drain carries its own expiry and releases itself when the session that set it dies. Deployed; no real expiry has happened yet.
- Advance preparation helps only if it is ready before the person starts. When the person does it first, the prepared item is withdrawn with one line. Earlier preparation has not run yet.
- Agent-written fields shown to the person get contracts (purpose, length, shape, banned patterns, examples) checked at write time. Not run yet.
- A send cut off by a timeout has an unknown result, not a failed one. It is not sent again, and the turn ends after the send returns. Fixed in the gateway (#208); no check yet that duplicate replies stopped.
- Checks that must run on the shared host run one at a time below a set load, and status polling is left to landing scripts. Written as a rule; its effect on load has not been measured.

## Dropped

| Pattern | Reason |
| --- | --- |
| N1. Never make the person wait | A turn-budget contract of the Go core. |
| N2. Typed results and a preview panel | App surface, owned by lina-app. |
| N3. Short replies enforced at generation | A Go core contract with no operating lesson behind it. |
| N4. What needs me, and progress as lines | App surface, owned by lina-app. |
| N5. Parallel coding hand-off | Product direction, now issues #35 and #63. |
| N6. Signed catalog and tool-definition hashes | Go core plugin contract. The fork uses Hermes MCP. |
| N7. Subscription-aware accounts | Product direction, now magpie in #32. |
| N8. Records as files in Git | Product direction with no operating lesson behind it. |
| N9. Harness improvement as a development loop | The Go `lina replay` command, which the fork does not have. |
| N10. Autonomy as data | Go core grant contracts. Covered by P11. |
| N11. Archive first, interpret second | Product direction, now #24. |
| What LINA drops (old table) | Listed Hermes parts the Go core replaced. The fork keeps them. |
| Lessons from earlier attempts (old table) | Folded into P1, P3, P8, P9 and P10. |
