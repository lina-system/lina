# Work panel research

Research for [#604](https://github.com/thisisjun786/lina/issues/604), read on 2026-10-22 and 2026-10-23. The question: how should LINA show many pieces of work at once, and the steps inside each? The proposal under test was one manager per 일, a step graph inside, a numbered stage list outside, and a TUI work panel with one row per 일. On 2026-10-23 the owner set the panel's form: a sidebar beside the conversation with one card per 일 holding its name, elapsed time and what it is doing now. The resulting design is in [work-and-delegation.md](../design/work-and-delegation.md#step-graph), [surfaces.md](../design/surfaces.md#progress-at-a-glance) and [tui.md](../design/tui.md#work-panel-and-stage-list-t-16).

## Findings

1. Cards must follow the 일, not the worker sessions. Across the owner's repositories a median of 18 pull requests (worker sessions) were open at once, with 41 at the 90th percentile. They belonged to a median of 5 projects, never more than 9. One card per project cuts the count by 4.5 times at the median.
2. Plans are wide and shallow. The owner's milestones have 1 to 3 dependency layers, and one layer held 82 issues. Numbering stages by depth gives a short list. Collapsing parallel steps into one entry is required, not optional.
3. The owner asks "what is running" often, and the answer is slow. On the assistant's human channels this was up to 19.5 times per active day. The median answer took 24 s and 6 tool calls, the 90th percentile 173 s and 30 tool calls. A panel that is already current replaces that work.
4. Comparable products agree on the row. Nearly every session list shows a state, a title, an age and one line of current activity, puts what needs the person first, and folds finished items into a count. That is the owner's card. Detail opens on request.
5. They agree on the method for plans. The two orchestrators number stages by the longest prerequisite chain in code and show lists, not graphs, in the terminal. Where a graph is drawn, a narrow pane fits about 2 parallel steps before it scrolls sideways. No session list collapses parallel steps into one stage, and two products turned off by default the checklist the model writes itself, which users call stale. So Core, not the model, keeps the stage list.
6. Users complain about silence, not about missing graphs. The recurring complaints are work that looks hung (no state, no elapsed time, queueing that cannot be seen), needs-you marks that outlive their cause, items that drop off the list, background output flooding the main view, and panes that open empty.

## Our records

### Sources and method

| Source | Scope | Stands for |
| --- | --- | --- |
| GitHub pull requests and issues | 590 pull requests in LINA, devflow and five other repositories of the owner, read 2026-10-22 19:50 UTC | A pull request's open interval is one worker session. Its repository and the milestone of the issue it closes stand for the 일. |
| devflow job records on one development host | 105 jobs over 3.5 days | Worker sessions from start to done and landing |
| devflow landing digest | 67 outcome lines, 21:00 to 04:44 KST | What the owner reads when work lands |
| The owner's assistant conversation records | Three assistant profiles, messages since 2026-09-01; records begin 2026-10-11 | How often the owner asks what is running, and how the answer is produced |

Concurrency is sampled every 5 minutes from 2026-10-20 00:00 to 2026-10-22 19:50 UTC (815 samples). The conversation records were read by a read-only script that prints counts and latencies only, never message text.

### Concurrency

| Measure | All repositories | LINA |
| --- | ---: | ---: |
| Open pull requests, median / 90th pct / max | 18 / 41 / 56 | 3 / 21 / 37 |
| Projects (repository and milestone), median / 90th pct / max | 5 / 6 / 9 | 1 / 3 / 4 |
| Pull requests per project, median | 4.5 | 5.7 |
| At the time of reading | 31 open under 6 projects | 19 open under 3 milestones |

On the one host with job records, at least one job ran 67% of the time. Concurrent jobs had a median of 4, a 90th percentile of 12 and a maximum of 17, across at most 4 repositories. A job ran 41 minutes at the median and 270 at the 90th percentile, then waited 9 minutes (median) or 85 (90th percentile) to land. 19 of 105 jobs (18%) were stacked on an open predecessor. LINA merged 199 pull requests in the last 7 days, each open 68 minutes at the median and 320 at the 90th percentile. All repositories together merged 317 in 7 days; LINA alone merged up to 15 in one hour.

### Shape of the plans

The "Depends on" lines of the LINA issues give the following layers per milestone. A layer is the set of issues at the same depth of the longest prerequisite chain.

| Milestone | Issues | Dependency links | Layers | Issues per layer |
| --- | ---: | ---: | ---: | --- |
| M1 | 147 | 10 | 2 | 140, 7 |
| M2 | 101 | 19 | 3 | 82, 17, 2 |
| M3 | 54 | 8 | 2 | 46, 8 |

Links are declared sparsely, and write-area conflicts also order work, so real chains are somewhat deeper. The shape still holds: few stages and very wide ones.

### What the owner reads

The landing digest had 67 outcome lines in 7 hours 44 minutes, about 8.7 an hour, across 4 repositories. A line had a median of 23 characters and a maximum of 53. Reporting each landing as a notice would interrupt every 7 minutes. That is why a landing is a row change, not a notice.

### How often the owner asks, and how the answer is made

Questions were matched on status words ("뭐하고 있", "진행 상황", "어디까지", "현황", "status", "progress") and on what-needs-me words. "진행해" and other instructions were excluded.

| Profile, human channels | Owner messages | Questions | Share | Per active day | Answer median / 90th pct | Tool calls before the answer, median / 90th pct |
| --- | ---: | ---: | ---: | ---: | --- | --- |
| Assistant that runs the work | 7,519 | 234 | 3.1% | 19.5 (12 days) | 24 s / 173 s | 6 / 30 |
| General assistant | 11,270 | 240 | 2.1% | 18.5 (13 days) | 43 s / 180 s | 45 / 366 |
| LINA profile | 629 | 9 | 1.4% | 0.7 (13 days) | 16 s / 54 s | 2 / 8 |

Limits of this table:

- The numbers are upper bounds. Human channels still carry some bot-to-bot messages and system notes. Automated prompts (scheduled and one-shot events) are excluded. Counted with them, the first profile had 1,651 matches, 1,012 of them on one day of automation.
- An answer is the first text reply before the owner's next message. Only 76 of 234 questions in the first profile got one, because the owner often wrote again first.
- Word matching misses questions phrased otherwise and catches some that are not about running work.

## Comparable products

The products are described by kind. Their names and links are kept with the issue's working record, not in this repository. Two of them are source-available under licenses that forbid reuse here. They were read for ideas only, and nothing was copied.

| Kind | What it draws | What it hides | Numbers |
| --- | --- | --- | --- |
| A. Desktop agent app with a live plan graph | A switchable list or graph. Stages are numbered by longest chain. The list folds finished stages into one "N done" row. The header shows stage K of N. | Tool calls and reasoning, collapsed. A step's transcript opens on click. | Plans up to 256 steps. The side pane is 360 px wide and fits about 2 parallel steps. The terminal app shows only a stage list of 3 to 10 rows. It keeps 3 live sessions but shows one plan at a time. |
| B. Terminal multi-agent orchestrator with wave scheduling | One row per task, capped at 5 with "+N more". The graph widget is a header (wave i/N, done, running, failed) plus up to 12 rows sorted by state, and running rows are never cut. Dependencies appear as text ("after A, B"). | Child transcripts and tool calls, which can be read on request. Completions go to the model, not to the person. | 5 tasks per model lane by default. A global cap of max(8, 2 × cores). 64 nodes per graph run. The display refreshes every 250 ms. |
| C. Coding-agent CLIs, desktop apps and cloud agents with session lists (seven products) | A state icon or word, a title, an age and a one-line activity summary; often diff counts and a pull request label. Groups by state with needs-you on top, done folded into "… N more"; one product shows 5 rows with a count, another 30 before grouping. Plans are flat checklists (pending, in progress, done); one plan tool allows only one step in progress at a time. | Transcripts, diffs and logs behind a click or a peek | Up to 20 subagents, or 8 agents per prompt; activity summaries refresh at most every 15 s; 3 to 60 concurrent tasks by plan tier |
| D. Parallel session managers (four products) | One workspace per branch with its state, diff counts and pull request state; kanban or state groups; a hand or dot for approval | Diff, checks, terminal and logs | One caps the list at 10 sessions and polls every 500 ms |

Complaints in public trackers:

- B's tracker has 4,064 issues. Matching "status" gives 1,079 issues, "background" 768, "progress" 413 and "stuck" 226. Many were filed by the maintainer or a bot, so these counts are upper bounds. Typical complaints: background work shows no spinner, state or elapsed time and looks hung. A hidden concurrency cap leaves sessions "waiting" with no reason. Panes open empty. The model polls for status and burns context. One notification grew to 1.75 MB. A failure in the first wave skipped 54 dependents.
- A's trackers had 4 issues in their first 3 days and none about the graph. The maker made the graph the default view. Its own demo still describes a plan as a numbered list ("five steps, two at the same time").
- In C, one tracker returns 519 issues for "subagent visibility" and 586 for "todo list" (full-text search, noisy). Typical complaints: you cannot see what a subagent does or whether it is stuck; no progress for 15 minutes or more; background output floods the main chat; the checklist shows only 5 items or goes stale; week-old or idle items vanish from the list; grouping by repository buries active work; a needs-input dot stays after the work finished; users ask for a progress board beside the chat.
- In D: a waiting mark shown after the process died; tasks that die with no notice; a list that needs a compact mode past 10 sessions; no alert when a confirmation is needed; a flood of notifications on startup.

## What changed in the proposal

| Proposal | Evidence | Decision |
| --- | --- | --- |
| One manager per 일; the 일 is the unit of display | 4.5 to 5.7 sessions per project; 17 concurrent jobs on one host | Kept. Cards and the running count follow 일, not sessions. |
| A one-line row with name, stage words and n/m | The owner's correction of 2026-10-23; session lists in C and D show title, age and one line of activity | Changed. A card of two rows: the name with the elapsed time, then what it is doing now. No stage position on the card. The sidebar is always shown on a wide terminal and replaces the full-screen progress view. |
| Steps as a graph inside, deterministic scheduling by Core | Both orchestrators compute readiness in code. Waves are labels and never hold work back. | Kept. The frontier is computed from the ledger without a model call. Stages never gate work. |
| Parallel steps collapse into one numbered stage | Plans 1 to 3 layers deep and up to 82 wide | Kept. A done stage never reopens: a later step goes to a current or later stage. |
| The graph is never drawn | No product draws one in a terminal. A narrow pane fits about 2 parallel steps. | Kept. The acceptance check refuses edge and arrow characters. |
| Worker sessions only under the current stage | Products hide transcripts and open them on request | Kept. Sessions end when their step lands, so live sessions follow the frontier. |
| Order: needs you, running, waiting outside, done; fold done; cap rows | Products group by state, cap rows with "+N more" and never hide running rows | Kept, with one change: the overflow row gives only the hidden count and the hidden needs-you count, under the owner's wording rule. |
| A count such as "2 at once" on a parallel stage | The owner's wording rule: a few words per stage | Changed. The label names the first step and the number of the others. |
| Elapsed time | The owner's card; products show an age on every row; complaints about work that looks hung | Kept on the 일's card only. Worker rows under a stage show the worker and its state word, without time; run records stay in Details. |
| The model's own checklist as the plan display | Two products turned it off by default; users call it stale | Not used. The stage list comes from the ledger, and Now changes only on a step or wait event. |

## Follow-up measurement

Run the same aggregate count over the same channels for two weeks after the panel ships, and compare questions per active day with the upper bound of 19.5. The PTY checks of T-16 test the panel. They cannot show whether the person asks less.
