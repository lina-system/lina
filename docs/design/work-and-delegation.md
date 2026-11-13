# Work and delegation: how what the owner throws becomes work, who does it, and how it ends

The owner's decisions on the goal-operation structure, as rules. Each section gives the decision in the owner's terms,
why, and the issue that builds it. Most of it is not built yet; the linked issue says what is, and a section here
changes in the same pull request that builds it. Runner and model settings live in
[lina-work-settings.md](../lina-work-settings.md); message delivery lives in [message-routing.md](message-routing.md).

## 1. Four layers and five goal kinds

**Decision.** Work runs in four layers:

- **conversation**: the agent's main session, where the owner talks and throws work;
- **goal session**: one session per goal; it splits the work, hands it out, judges what comes back and talks to the
  owner, and never runs the work itself ([worker-card-session.md](../worker-card-session.md));
- **worker**: one run on a node, started by `lina worker` and judged by the goal session;
- **reviewer**: an agent that only reviews (section 10).

Every goal has one of five kinds, split by how "done" is judged. The kind picks a preset: who works, which gates run,
who reviews and what reaches the owner.

| Kind | Done when | The owner's part |
| --- | --- | --- |
| Development | the pull request is merged and `dev` CI is green | none |
| Research | a report with conclusion, evidence and open questions, plus the issues or decisions it leads to | publishing |
| Document | the document is in place and passes the format and word checks | signing, publishing |
| Creative | the owner picks | the pick, publishing |
| Operations | it is seen running, and the way back is written down | deletion, credentials |

Routine and idea are not goal kinds (section 3). Money, contacting people, permanent deletion, publishing and
credentials are not kinds either: they are an approval mark on a goal or task, and that step stops for the owner.

**Why.** Rules had drifted per agent; one small set of concepts, split by how done is judged, makes "done" checkable
the same way for every agent.

**Builds it:** lina#317.

## 2. One receiving place

**Decision.** Everything that comes in (the owner's words, another agent's message, an outside notice) goes through
one receiving place. It decides, in one step, whether the item is a new goal, an attachment to a goal that exists, an
owner's to-do, an idea, or plain talk handled aside with one result line; for work it also picks the kind, the size
and the worker (default `omo`). The owner never fills in a form: when the request is thin or hard to undo, the same
step reads the intent back to the owner in one or two lines, and the confirmation makes it a goal. Each decision is
one line on the place's timeline, so the main session sees the outcome without the message entering its turn.

**Why.** Each source went its own way and nothing decided, in one place, what an item becomes; asking the owner once,
at the door, replaces both the start-up form and a second question later.

**Builds it:** lina#520 (measured first by lina#519).

## 3. Routine and idea are their own kinds

**Decision.** Work that never ends is a **routine**: it never closes, its state is working or broken, and it is quiet
until it breaks (no notice and no highlight while it works). An **idea** is something an agent researches and then
asks "make it a goal?"; only the owner's yes makes a goal, linked back to the idea. Neither is a goal, so neither is in
the goal list or its counts.

**Why.** A routine counted as a goal looked unfinished forever and raised notices while nothing was wrong.

**Builds it:** lina#517.

## 4. A closed goal is never reopened

**Decision.** A goal that did not turn out right is not reopened. The owner says it again and a new goal is made,
linked to the one it follows (`--follows`); the old goal stays closed. A failure found after the close becomes a new
linked goal the same way. The ten-minute undo of a mistaken close stays.

**Why.** Done was decided by gates and a judge; reopening would undo a verdict nobody judged again, and the linked
goal keeps both attempts readable. A message on a finished goal follows the same rule
([message-routing.md](message-routing.md) section 3).

**Builds it:** lina#515.

## 5. The agent closes; the owner only checks for real

**Decision.** The agent finishes and closes its goal. When an agent's goal closes, one "check it for real" to-do
appears in the owner's own to-dos; confirming it closes it. If the check shows a problem, the owner starts a follow-up
goal from that to-do (section 4). A goal the owner closed or dropped creates no check.

**Why.** The owner's time goes to the one real look at the result, not to closing records.

**Builds it:** lina#516.

## 6. Groupings are made by the tool; milestones live on GitHub

**Decision.** Areas, projects and repositories are groupings the tool makes from tags and from where the results land;
the owner never files a goal into one. A goal is the owner's outcome in the owner's own words with a clear sign it was
reached; the area is an optional tag on it. A milestone is a code-side grouping that lives only on GitHub; it is not a
goal, and a development goal may carry a small tag naming its milestone.

**Why.** The milestone, area and project terms had grown tangled; the owner thinks in goals, and the grouping is
bookkeeping a tool can do.

**Builds it:** lina#439 (one model for stores, memory and work); the optional area tag is lina#514.

## 7. Agents prepare the owner's to-dos by field

**Decision.** An owner's to-do is assigned to the agent whose field it belongs to (default: the operations agent).
That agent does what it can alone first (a draft, a review, finding material) without asking, and writes one prepared
line on the to-do: what is done, where the results are, what still needs the owner. The agent that keeps the owner's
to-do list folds those lines into its usual briefing; the assigned agent sends no message of its own. A prepared
result starts as a draft and becomes final when the owner accepts it once in the app. Sending, paying, deleting and
publishing stay with the owner: they are only ever written as "needs the owner".

**Why.** The owner should meet a to-do already prepared, not as raw work, while the acts that cannot be taken back
stay with the owner.

**Builds it:** lina#526 (the assigned agent and the prepared line); the receiving place assigns it (lina#520); results
and the draft-to-final step are lina#527.

## 8. What a goal line shows

**Decision.** A goal line shows the owner agent, what it is doing now, the time since it started, and the worker and
model running it; never a run number or a round count. A mark appears only when something is wrong (stopped, a wait
past its time, a check left, a limit hit); no news alone is not wrong.

**Why.** Ids and counts mean nothing to the owner, and a mark for silence made most running goals look broken.

**Builds it:** lina#521.

## 9. Each handed-off item has its own goal

**Decision.** Every item handed off, big or small, gets its own goal. New work is never hung under a catch-all goal.

**Why.** A goal is one outcome with one sign that it was reached; a bundle of unrelated work has neither, so it can
never be judged done.

**Builds it:** lina#655 (it replaces "an agent's task can be made with no goal" in lina#514).

## 10. The reviewer agent

**Decision.** The reviewer agent takes no work. It only approves or sends back the cards in the review column,
against the done condition of the goal's kind (section 1).

**Why.** The agent that did the work does not approve it; one agent that only reviews keeps the done conditions
applied the same way for every kind.

**Builds it:** lina#657.

## 11. Personal source text

**Decision.** This is one rule for all of LINA. Personal source text (the owner's documents, messages, captures and
other raw material) may go to a worker and to the model services in use when the work needs it. It never goes to a
public place: public repositories, public issues, outside posts. Secrets are written nowhere.

**Why.** A limit on where raw text may travel inside LINA kept private work away from the workers that should do it;
the real risks are a public place and a written secret, so the rule names exactly those. Masking exists for secrets
and for what flows into prompts, not as a destination limit.

**Builds it:** lina#657 (the instructions); capture-specific tests stay in lina#498. Linked from
[lina-work-settings.md](../lina-work-settings.md) and [message-routing.md](message-routing.md) section 6.

## 12. A blocked item never stops independent work

**Decision.** An item that is blocked, or waiting on an answer, never stops the lines of work that do not depend on
it; they run at the same time.

**Why.** One question to the owner should cost the time of that line only, not of everything else in flight.

**Builds it:** lina#657.

## 13. Screen rules

The owner's screen rules (one detail frame for every detail view, history rows as `time · one sentence`, neutral work
wording, an in-progress line is one goal or task session) live in
[lina-app#123](https://github.com/lina-system/lina-app/issues/123) and are not restated here.
