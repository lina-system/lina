# M4-1 research: how the owner works with his agents and coding workers

This report generalizes what the LINA project owner's sessions with his eight current assistant agents and his coding workers show about asks, corrections, hand-offs and report shapes. It is evidence for the per-agent persona and work-method design (#588) and the baseline that the replay measurement (#590) checks against. It is not normative. The design contracts in [docs/design](../design/) stay the source of truth, and nothing here changes them.

The detailed per-agent notes, the source references (store, session and message ids) and the replay set stay private on the owner's host, outside every repository. This file contains no message text, no persona names, no third-party names, accounts or hosts. Agents are named by the preset role they map to in #581: system, LINA, dev, knowledge, life, creative, investing and legal.

## Method

- Sources, read-only: the eight agents' session stores and lossless-context stores, their persona files, memories (including the memory-service notes about the owner) and skills; the owner-facing Codex and Claude Code session logs; the devflow briefs, reports, event logs and the digest.
- Turns written by the system were removed before counting: context-compaction summaries injected as user turns, an agent's own delegated briefs and headless prompts, cron and relay messages, and duplicates re-inserted after restarts. In the worker logs, briefs written by agents, hook injections and relays between sessions were separated from what the owner typed.
- Every owner turn was classified by request type. Each correction was classified as length (too long, too short or the wrong format), asking (asked when he wanted action, or acted when he only asked), naming (wrong names, terms or language), done-claim (claimed done without evidence, or it was not done) or other (wrong target, wrong fact, tone, scope, hand-off failure).
- Each finding in the private notes is marked observed, inferred or unknown with its source reference. Below, "observed" means seen repeatedly in the data and "inferred" means concluded from several observations.

## Numbers

| Role | Owner turns | Sessions with the owner | Date range | Corrections (length / asking / naming / done-claim / other) | Replay cases |
| --- | --- | --- | --- | --- | --- |
| system | about 795 | 8 | 2026-10-11 to 10-23 | about 90 (4 / 12 / 11 / 20 / 43) | 89 |
| LINA | 294 | 9 | 2026-10-11 to 10-22 | 55 (14 / 3 / 6 / 8 / 24) | 100 |
| dev | 828 | 5 | 2026-10-12 to 10-23 | about 56 (7 / 10 / 12 / 7 / 20) | 72 |
| knowledge | 188 | 4 | 2026-10-12 to 10-23 | 50 (6 / 7 / 8 / 3 / 26) | 68 |
| life | about 110 | 4 | 2026-10-12 to 10-23 | 16 (0 / 4 / 1 / 0 / 11) | 66 |
| creative | 87 | 2 | 2026-10-19 to 10-23 | 23 (0 / 4 / 0 / 0 / 19), plus 12 soft re-asks | 63 |
| investing | 4 | 1 | 2026-10-19 to 10-21 | 0 | 11 |
| legal | 3 | 1 | 2026-10-19 to 10-20 | 0 (7 on legal-type work done by the LINA role) | 34, of which 12 are native |
| coding workers | about 5,400 Codex and 560 Claude Code turns typed by the owner, from 643 Codex sessions and 162 Claude Code session files | — | 2026-08-05 to 10-22 | about 164, estimated from a sample (10 / 25 / 60 / 14 / 55) | 80 |

- The agents hold 1,983 stored sessions. Only 34 of them contain owner turns; the rest are cron runs, delegated runs and agent-to-agent sessions.
- About 2,300 owner turns were read in the agents' stores. Of the 20,757 user turns in owner-facing Codex sessions, about 26 percent are the owner typing.
- devflow: 321 briefs and 105 runs (47 development, 52 QA, 6 other). 32 of the 47 development runs landed. The 3 failed runs never started. The 10 blocked events came from missing prerequisites, CI still red after three rounds, and faults in the done gate.
- The replay set has 583 cases: 300 corrections and 283 accepted replies, 234 of them marked as behaviour that tailoring must not change. The investing and legal roles cannot reach 30 native cases. Both were set up four days before the end of the data and the owner barely used them. Their sets are completed with the same kind of work the owner gave to the LINA and dev roles, marked by origin.
- The correction rate per owner turn is 7 to 27 percent across the agents with data: dev 7, system 11, life 13, knowledge 27 and LINA 19 percent. It did not fall over the eleven days, although the agents wrote their corrections into their own skills and memories.

## Shared patterns

### Report length and shape

- Observed: the owner's messages are very short (median 20 to 32 characters per role) and the agents' replies are 15 to 25 times longer (median about 500 to 700 characters).
- Observed: length complaints concentrate on status reports, notifications and documents meant for third parties (mail, invoices, form remarks, notification layout). Comparisons, research answers and creative candidates were long and structured, and drew no length complaint. Compression was corrected too: a weekly report cut too short, a narrative draft too thin, a design analysis skimmed.
- Inferred: length depends on the reply type, not the agent. Status, notices and third-party documents need to be short and stripped. Comparisons, research and creative candidates can be longer, but must lead with the conclusion.
- Observed, accepted everywhere: the verdict or result first, then a table for comparisons and costs or a few short items, then one next step. A scheduled check whose first line is "normal" or "N need checking", followed by at most three lines per problem item and nothing about normal items.
- Observed, rejected everywhere: internal codes, ids and run identifiers in the owner's view; narration in another language; meta-talk about the agent's own settings or process; a fixed closing line saying there is nothing to decide; tables in a chat channel for a status answer.

### When to ask

- Observed: the main asking complaint in every role with data is the same. The agent asked, or handed a step back to the owner, when it could have done the step itself or found the answer in a record it could read.
- Observed: the opposite complaint is narrow. A question ("is it possible", "why") or a direction the owner was still thinking aloud was taken as an order. No complaint was ever about an agent waiting for a go-ahead before sending, deleting, paying or publishing. In the LINA role every send followed an explicit go-ahead, and none drew a complaint.
- Inferred: one rule fits every role. Do reversible work and say so in one line. Ask once, with one recommendation, only for irreversible, external, costly or public steps, or for a fact only the owner has. Answer a question without acting on it, and carry out an imperative. This matches rule 1 of the preset methodologies in [conversation-and-memory.md](../design/conversation-and-memory.md#preset-methodologies), and adds the question-versus-order distinction.
- Observed: when a question is unavoidable, one narrowed question or a short multiple choice works. Open question batteries and questions at the end of a long answer go unanswered, and proceeding with the stated default was accepted.
- Observed: the owner rejects extra wrappers, locks and stricter gates on his own tools. The guards he keeps are on credentials, deletion, money, publishing and content safety.

### What counts as done

- Observed: done-claim corrections cluster in the system, LINA, dev and knowledge roles and in the coding workers. Every case follows the same pattern: the agent checked a proxy layer instead of the layer the owner uses. Examples of proxy layers are a merge, a passing CI run, a script that exited cleanly, stored credentials, a build that was not running, or another agent's report.
- Observed, accepted in every role: saying in the same reply what was checked directly, what was estimated and what was not checked, with counts. Admitting a mistake in one sentence and giving the fix. The dev role used such source labels more than 500 times without a single complaint.
- Inferred, evidence per kind of work:
  - service or setting: restarted, with the value read back from the running process
  - interface: opened in a real window at the real size
  - data or collector: the live value compared with what is displayed, with gaps and time zone checked
  - message: the sent copy reopened
  - code: the owner's path actually run; otherwise "merged, not yet live", as dev methodology rule 7 already says
  - anything another agent or worker reported: labelled as a claim until reproduced

### Naming and language

- Observed: every line the owner sees must be in his language, including progress narration. Narration leaking in another language drew complaints in the system, knowledge and dev roles and in one coding worker. In that worker it recurred after an apology.
- Observed: one retired device name drifted into four different agents. Business entity names, artifact names and the owner's own labels in documents were corrected in several roles.
- Inferred: naming is a shared glossary that the person owns and every agent reads, not a per-persona trait.

### Persona voice

- Observed: the owner keeps each persona's voice on every topic. A persona rule that switched to a formal register for serious topics drew his only explicit tone correction in that role. Tone friction came from moralizing, repeated warnings and jokes placed over a real question, never from the voice itself.

### Hand-offs

- Observed, worked: a self-contained brief (goal, decisions already made, end condition, the one step the owner must take), with the sending agent checking the result itself before reporting.
- Observed, broke:
  - a hand-off announced but never sent
  - no check-back when the receiver finished, so the owner had to poll
  - an owner decision relayed that the owner had not made
  - a busy receiver with no queue
  - two agents changing the same artifact in parallel
  - two channel sessions of the same agent holding different decisions
  - questions meant for the legal role that never reached it
- Observed: real routing differs from the role plan. Legal, contract and accounting work went mostly to the LINA and dev roles, and early investing questions went to the LINA role. Device setup went to the dev and life roles as well as the system role, and the creative role ran its own tool installs.

### Failures that belong to Core, not to a persona

- Observed: after context compaction, old replies were posted again (knowledge, creative and dev roles). Requests were lost or interrupted without notice, and long jobs ran without any progress line, so the owner polled.
- Unknown: whether memory-service review notices appended to chats reached the owner's channel. If they did, they conflict with his rejection of meta-talk.

## Per role

- **system.** Most asks are about setting up and repairing the agents' own installation. Frequent complaints: restart and apply results reported without proof from the running process, over-guarding, handing a runnable step back to the owner, and decisions taken against a standing rule he set for all agents. Accepted: verdict first, then verified facts and one open item; backup before change; asking before breaking a rule that applies to all agents.
- **LINA.** Most asks are mail, documents, to-dos and coordination. The highest correction rate. It is driven by surplus content in documents and notices rather than missing content, and by stale status lists made without opening the original source. Accepted: draft, show, send on the go-ahead, verify the sent copy, update the to-do; an honest "not found" before guessing; one recommendation with its condition.
- **dev.** Most asks are delegating, watching and merging. Complaints: claiming done at merge or CI, writing a claim before the command returned, passing a worker's claim along as fact, internal codes in status replies, idle capacity the owner had to point out, and asking about reversible steps. Accepted: recommendation with a default, source labels, measured numbers, real-run checks with one screenshot.
- **knowledge.** Most asks are people records and private writing, and the classic wiki upkeep was rarely asked for. Complaints: narration in another language, moralizing and repeated warnings, asking when a readable source existed, and declaring a backlog empty without checking. Accepted: separating records by sender, dated non-destructive corrections, verification with numbers.
- **life.** Asks are diet and health logging, device comparisons and hobby records. Complaints: forgetting stated preferences, guessing the owner's own device facts instead of checking them, giving up too early on a tool, and a register switch on a serious topic. No length complaint at all for tables with a named baseline.
- **creative.** Asks are lyrics, images and the tooling behind them. Complaints: hand-offs that were not sent or not followed up, the agent's favourite candidate rejected (overruled in at least 8 of 9 documented picks, accepted once), and asking before reversible reruns. Accepted: opening the original output before judging, three candidates of different types, one narrowed question after a vague rejection.
- **investing.** Two research answers, both accepted. The conclusion came first, with sourced figures, scenarios, dated next checks and no verdict. Two closing questions went unanswered. The no-orders line was tested twice and held. Too little data for anything else.
- **legal.** One broad study order, done without questions and with an explicit list of unconfirmed items. The draft-only limit held everywhere, including where the LINA role did legal-type work. Advice was dropped within an hour when the owner changed direction.
- **coding workers.** The largest correction classes are wrong target (misread scope, especially around negations) and language. Questions were also treated as orders, and orders as questions. Accepted: the mid-point check format the owner asked for, honest "not verified" footers, numbered options with one marked recommendation, delegated merges reported with the commit. The devflow reports are long evidence records for the dev agent. The owner sees one digest line per feature, and that shape drew no complaint.

## Conflicts to resolve in #588

1. **One recommendation or a set of options.** Most roles should give one recommendation. For creative selection the owner expects several candidates of different types and usually picks against the agent's favourite. Creative selection is a separate case.
2. **The "nothing to decide" line.** It is rejected as a closing line, which [Reply shape](../design/conversation-and-memory.md#reply-shape) already forbids. It was accepted inside a status format the owner asked for.
3. **Long answers.** The research and comparison roles were accepted at lengths the status roles were corrected for. The budget belongs to the reply type and the person, not the agent.
4. **Register on serious topics.** One persona text switches register by topic, and the owner rejected it. Persona voice should stay the same across topics.
5. **Overlapping roles.** LINA and legal (contracts, invoices, tax arithmetic), LINA and investing, system and the dev and life roles (device setup), dev and creative (fixes to the same creative tool). Each kind of artifact needs one owning role and an explicit route.
6. **One agent, several channels.** Decisions made in one channel session were not visible in another session of the same agent.

## What this suggests for the design

These points are evidence for #588, not decisions:

- The data supports the eight inherited methodology rules. The changes it argues for:
  - Rule 2 could say that "done" means observed at the layer the person uses, with evidence by kind of work.
  - A rule could cover the person's language for every visible line, including progress lines.
  - The question-versus-order distinction could be added to rule 1.
- A person-owned glossary of names (devices, entities, artifacts) shared by all agents.
- A hand-off record:
  - "sent" only on a delivery receipt
  - the sender checks back at the end condition
  - the owner's decisions relayed as quoted words with their time
  - one owner per artifact
- Persona text should not switch voice by topic, procedures stay out of it, and the work method stays data on the agent, as #588 requires.
- For #590, the replay set contains measurable expectations per case: length within a budget, no question on a reversible step, no internal code or wrong name, and no "done" without an evidence clause. A scorer must first drop the turns that the system injected as user turns (compaction summaries, delegated briefs). Otherwise the asking and length scores are skewed.

## Limits

- The agents' data covers 13 days. Coding worker data covers 11 weeks.
- The investing and legal roles have too little native data, so their findings rest on one or two exchanges and on how other roles handled the same kind of work.
- The memory service was not reachable. Its conclusions were read from the notes already stored in each agent's memory.
- Whether progress lines, re-posted replies and memory-service notices reached the owner's channel is unknown. The stores record them, but not whether they were delivered.
- Long agent replies were clipped at about 2,500 characters in extraction, so reported lengths are lower bounds.
- Briefs written by the dev agent could not be told apart from the owner's own typing in some worker logs. The worker counts are estimates from a sample.
