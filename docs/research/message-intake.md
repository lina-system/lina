# Research: which 일 a message is about

A person writes one short message. LINA has to decide what it is about: the current conversation, a new 할 일, a change to an existing 일 or 목표, a steer to running work, a question about progress, or something to remember. This note gives the evidence for that decision and defines how it is measured. It feeds the design in [conversation-and-memory.md](../design/conversation-and-memory.md#what-a-message-is-about) and [work-and-delegation.md](../design/work-and-delegation.md#messages-about-work) (#775). It is not normative.

The owner's messages, the labelled cases and the per-case notes stay private on the owner's machine. This file holds no message text, no persona names, and no names of people, accounts or hosts. Agents are named by preset role: system, LINA, dev, knowledge, life, creative, investing and legal.

## Method

- **Owner data.** Every message the owner typed to his eight assistant agents from 2026-10-11 to 10-23: 2,596 user-role rows. 283 of them (11 percent) were not the owner: compaction summaries, an agent's own briefs and rows re-inserted after restarts. The remaining 2,313 messages were labelled with what they were about, the cue that decided it, and whether the agent's reply picked the right target, judged from the six turns before and the three after.
- **Fixtures.** A second pass built a state fixture for 442 messages: 200 drawn at random and 242 hard ones (about running or earlier work, readable two ways, or picked wrong). A fixture lists the items alive at that moment, the correct target, whether a careful agent should have asked, the surface signals a rule could see, and what the agent did.
- **Labelling.** Model-assisted, by parallel labellers working from one written brief each pass. A manual check of 25 random labels agreed on the target in 23 and was unsure in 2.
- **Rule replay.** Scripts apply each candidate rule to facts Core can see without reading meaning (platform reply-to, arrival while the agent ran, time gaps, the agent's last turn, explicit references, word lists) and compare with the labels.
- **Outside evidence.** Public product documentation and papers, listed under Sources.

## What messages are about

| Target | Share of 2,313 messages | Wrong picks by the agents |
| --- | --- | --- |
| The current conversation (answers, approvals, corrections, follow-ups) | 62.7% | 28 |
| A new 할 일 or piece of work | 10.3% | 5 |
| Steer to work running now | 7.8% | 2 |
| An existing 일 that is not the current topic | 7.5% | 5 |
| A question about progress | 6.3% | 4 |
| Chat | 3.3% | 0 |
| Something to remember | 1.8% | 1 |

- About one message in five (21.6 percent) referred to running or earlier work. The dev role had the most (36 percent of its messages), because it runs long jobs. The system role had 15 percent and the other roles 10 to 18 percent.
- 39 percent of messages had a plausible second reading. The common pairs were the current conversation or new work (223), the current conversation or a steer (154), new or earlier work (131) and the current conversation or an earlier 일 (102).

## Cues

| Cue | Share | What those messages were about | Decides by rule? |
| --- | --- | --- | --- |
| Platform reply to an earlier message | 2.2% | The quoted message's item in all 50 | Yes |
| Explicit reference (number, link, file, quoted title) | 4.8% | The referenced item | Yes, when it resolves to one item |
| A named project or thing | 7.1% | Earlier work in about half, the current topic or new work otherwise | Yes, against a registry of names; no registry existed |
| Answer to the agent's question or proposal | 16.2% | The current conversation in 97 percent | Only for an offered option by number or label; free answers need the model |
| Arrived while the agent was busy | 2.9% | The running work in about six in ten; the rest were questions, replies or new requests | No |
| Pronoun or dropped object (그거, 아까, 이것도) | 7.6% as main cue, 18% present | The current conversation 63 percent, earlier work 17 percent, new work 8 percent, a steer 5 percent | No |
| Status words | 4.9% | Progress questions, recognised by meaning; a word list was right 80 percent of the time | No |
| Remember words | 1.0% | 31 of 42 remember messages had no such word, and the words often appear in questions about the past | No |
| New imperative with no link to the topic | 7.3% | New work | No |
| Recency only (no marker) | 28.8% | The current conversation 84 percent | Prior only |

Time since the previous turn is the strongest single signal:

| Gap | Messages | Current conversation | New | Earlier 일 | Steer | Progress |
| --- | --- | --- | --- | --- | --- | --- |
| under 2 minutes | 1,425 | 72% | 7% | 5% | 9% | 3% |
| 2 to 10 minutes | 503 | 61% | 11% | 8% | 9% | 7% |
| 10 to 60 minutes | 246 | 41% | 20% | 13% | 5% | 17% |
| 1 to 6 hours | 95 | 19% | 26% | 28% | 1% | 17% |
| 6 hours or more | 17 | 12% | 29% | 24% | 0% | 18% |

Rules on surface facts alone decided few messages well:

| Rule, over the 2,313 messages | Coverage | Agreed with the label |
| --- | --- | --- |
| Platform reply-to binds to the quoted message's item | 2.2% | 100% |
| Status words mean a progress question | 3.0% | 80% |
| The agent asked, and a short reply came within 30 minutes: an answer | 1.5% | 79% |
| A short message within 2 minutes continues the conversation | 36.9% | 78% |
| A message while the agent is busy is a steer | 2.9% | 64% |
| Remember words mean something to remember | 1.6% | 29% |

On the 442 fixtures, at the level of the item:

| Signal | Fixtures | Right item |
| --- | --- | --- |
| Platform reply-to | 11 | 11 |
| Answer to the agent's open question | 52 | 51. The miss started new work from the chosen option. |
| Exact name or reference of one candidate | 33 | 30. Two misses asked for new work under the named item, one of them a running job. The third cited a finished item as evidence about the focus. |
| Arrived while a job ran, bound to that job | 61 | 49 |

- **What settled the target**, in the 200 random fixtures: the message simply continued the focus (48.5 percent), answered the agent's question (17), needed its content understood (26), named an item (3), arrived during a run (3) or replied to a message (2.5). Facts a rule can read (an answer, a name, a reply) settled about one message in five. The content had to be understood for one in four, and the rest simply continued the focus, which the model confirms.
- **Where the target was.** It was the focus in 75 percent of random and 52 percent of hard fixtures. When it was another existing item, ranking running and waiting items first and then by last activity put it first in 67 of 83 cases and always in the top three.
- **Several requests.** 12 percent of messages carried more than one request.
- **Questions.** No fixture needed one. In none did two targets tie while a wrong pick would have steered running work, started work at once, or sent, paid or deleted. The agents asked which item once in 322 judged fixtures, and that question was not needed.

## Where the agents picked wrong

The agents picked the wrong target for 45 of 2,254 judged messages (2.0 percent). Only 12 were a wrong choice of item or kind:

- **Delivery, 23.** The message was never processed (15), or older replies were posted again after context compaction (8). A stop order and its reversal never reached the running session. These are Core failures, not picking failures, and they argue for a durable record of every input and its target.
- **Misread inside the current topic, 10.** The item was right and the request was misunderstood.
- **Wrong item or referent, 7.** Five were the wrong machine, tool or claim behind a name; two changed the wrong one of two similar items. Names of machines, tools and bots were the recurring confusion.
- **Wrong kind, 5.** A permissive remark or a goal statement was taken as an order (2), a one-off request was set up as standing work (1), and the agent asked instead of acting on an approval (2).

The labels see only three turns after each message, so later corrections are missed and the rate is a lower bound.

## How products attach a message

| Product | What attaches a message | Inference | Asks | Correction |
| --- | --- | --- | --- | --- |
| Email (RFC 5322, RFC 5256, Gmail) | In-Reply-To and References headers | Subject and a time window as a fallback. Gmail narrowed this in 2019: when References is present, it must point into the thread | Never | Not documented |
| Slack, Telegram, Discord | The thread or the replied message | None | Never | Not documented |
| Codex, Devin, Copilot coding agent | The session the person types in. A message to a running session steers it now or queues for the next run, by the person's setting; Copilot applies a steer after the current tool call | None | Never | Queued messages can be edited, reordered or deleted; a new task is a separate action |
| Todoist quick add | A `#project` token | None | Only when the token matches nothing: pick a project or create one | Not documented |
| Voice assistants | A follow-up window (about 8 seconds for Google) | Alexa carries context across turns, 9 percent better F1 than its rules | A learned model decides; only about 23 percent of ambiguous requests needed a question | Repeat the request |
| Claude projects | Where the chat was started | None | Never | Move the chat to another project in one action |

Products attach by place or by token. Inference appears only in voice assistants and as the email fallback, and Gmail narrowed its fallback. Nobody guesses which running job a message is for: the person types in that job's session. No product documents an undo, but moving is one step where it exists.

## Methods for short, context-dependent messages

- **Which earlier message a message answers.** In 77,563 annotated chat messages, 94.9 percent of the next messages in a conversation came within 2 minutes, yet conversations spanned days. Linking each message to the one before it reached a link F-score of 35.0. The best model reached 73.5 on links and only 38.0 on whole conversations (Kummerfeld et al. 2019); a later model reached 51.7 (Ma et al. 2022). Time is a strong prior and a poor rule.
- **Dropped words.** Korean casual speech drops 50.1 percent of arguments, including 77.9 percent of transitive subjects and 24 percent of objects (Lee 2015). Rewriting a context-dependent question into a standalone one remains hard: pronoun substitution alone cannot fix two thirds of them (CANARD), and human rewrites beat automatic ones by 26 to 35 percent in retrieval (TREC CAsT 2019). Choosing among a short list of candidates is easier than rewriting the message.
- **Carrying state across turns.** Zero-shot GPT-4 tracked every slot of a multi-domain turn in 38.7 percent of turns on MultiWOZ (Li et al. 2024).
- **"None of these" is the hard class.** On CLINC150, classifiers with about 96 percent in-scope accuracy recalled 40 to 52 percent of out-of-scope requests, and the best system 66 percent (Larson et al. 2019). Large models average 25 to 64 percent (Arora et al. 2024). For LINA, a request that matches no candidate is the "new" class, and it needs its own check.
- **When to ask.** Asking on every ambiguity scores an F1 of about 37 when only 23 percent of cases need a question (Kim et al. 2021). A learned question selector helped on 45 percent of query facets and hurt on 19 percent (Qulac). Models judge their own ambiguity close to chance: 54 percent accuracy (CLAMBER), and without careful prompting they either never ask or almost always ask (Ambig-SWE). Stated confidence sits between 80 and 100 percent whether right or wrong (Xiong et al. 2024). A question should follow from a tie between candidates and the cost of a wrong pick, not from the model's confidence.
- **Evaluation.** Published work scores the target, the ask decision (precision and recall, or false-positive and false-negative rates) and sometimes a cost-weighted score, separately and per slice, since scores vary from 11 to 40 F across samples (Kummerfeld et al. 2019). No public benchmark scores attaching versus creating for assistant messages, so the replay set is built from the owner's own messages.

## Replay evaluation

The intake replay scores any router on real messages before a change enters use. A router can be the Core rules, a judge, the model, or all of them together.

**Case.** One message of the person, with:

- the state Core would have had: the focus and its age, the candidate items (kind, state, last activity), open questions with their offered options, running tasks and runs, and the surface facts (reply-to target, thread or card, time since the previous turn);
- the recent turns, for the model;
- the gold target (an item, new or none), its kind, the 일 or 목표 a new item belongs to, and whether a question was warranted;
- what the agent did, for the baseline.

A question is warranted only when two targets still tie after everything Core and a careful reader know, and a wrong pick would steer running work, start work at once, or send, pay or delete. Filing that one tap can move does not warrant a question.

**Scores**, per router and set:

- **Pass rate** (primary): asked exactly when a question was warranted, and otherwise picked the gold target.
- **Right item**: the share of picks made without a question whose target is the gold target.
- **New or existing**: the share whose class (none, new, an existing item) is right.
- **Questions**: questions asked when warranted, and needless questions.
- **Silent wrong picks**: a wrong target without a question, by gold and picked class, with running work counted separately.
- **Per decider**: coverage and precision of each Core rule and of the judge.

**Sets.** Random messages measure everyday behaviour. Hard messages (work-referring, readable two ways, or picked wrong) measure the cases that matter. Both are scored separately and never pooled.

**Baseline**, on the 442 fixtures:

| Router | Random, 200 | Hard, 242 |
| --- | --- | --- |
| The agents as they behaved (judged cases only) | 98.7% of 152 | 94.7% of 170 |
| Always the focus | 75.5% | 52.5% |
| Core rules, then the focus | 77.5% | 59.5% |
| Core rules, then a perfect model (ceiling) | 99.0% | 99.2% |

- The current agents keep one conversation each, with the whole thread in context, and rarely picked wrong. Their silent wrong picks were 1.3 percent of judged random and 4.7 percent of judged hard fixtures, and they asked one needless question. About a quarter of the cases could not be judged from three after-turns.
- The focus alone misses a quarter of everyday messages and half of the hard ones. Rules add a few points, so most of the gain must come from the model's part.
- The ceiling shows what the rules cost: four fixtures, under one percent. Three were new work under the item a rule found, and one cited a finished item.

**Use.** The replay runs before any change to the rules, the candidate ranking, a judge, its threshold or the instructions enters use. A change ships only when the pass rate holds on both sets, silent wrong picks on running work do not rise, and the reply-to and offered-option rules stay exact. The first implementation must at least match the current agents on both sets. The case set stays private and grows as the standing analysis adds the owner's new messages. Bindings the person moves in LINA become new cases.

### Replay file format

Run `lina intake-replay /path/to/cases.jsonl`. The file is UTF-8 JSON Lines,
one version 1 case per line, up to 1 MiB per case. A malformed or oversized line
is counted and later lines still run. Empty, missing and unreadable files are
counted too. Unknown fields, invalid targets, duplicate case/item ids and dangling
question or surface references are rejected. The shared Go types in
[`internal/core/intakereplay/types.go`](../../internal/core/intakereplay/types.go)
define the format. The [synthetic set](../../internal/core/intakereplay/testdata/synthetic.jsonl)
is a complete public example. Keep private cases and output outside the repository.

Each case contains:

- `version: 1`, an `id`, and `set: random` or `hard`.
- `state`: a fixed RFC3339 `clock`, `focus` and `focus_age_seconds`, `items`,
  `questions` and `surfaces`. Items have id, title, kind, state, last activity,
  optional names and links. Kinds are initiative, project, todo, task or run.
  Work snapshots include running, waiting and needs-input states. Questions
  identify their target and offered labels, optionally `yes_no`. Surface keys
  identify a reply message, card or thread and map to an item id.
- `input`: text, optional reply/card/thread surface keys, the gap in seconds
  since the previous turn, and recent person/agent turns.
- `gold`: one target and ask decision per request, in message order. Targets
  have class `existing`, `new` or `none`; existing targets carry id and kind,
  new targets carry kind and optionally their parent item id.
- Optional `observed` bindings retain what the original agent did for later
  comparisons. Optional `expected` scores pin the two public baselines in CI;
  neither field is given to a router.

Output contains selected, scored and excluded case counts, exclusion reasons,
per-case bindings and scores, and summaries keyed by set, router and decider.
It omits input and recent-turn text. Each rate includes its numerator and
denominator; a zero denominator produces null, never a claimed success.

The `all` row's pass rate is per message: every request must have a binding,
no extras, an ask exactly when warranted, and otherwise the exact gold target
(including kind and new-item parent). Right-item and new/existing rates use
unquestioned picks as their denominator. Questions report warranted, correctly
asked and needless counts. Silent wrong picks include a class confusion table
and a separate count when either the gold or picked target is running or
waiting work. A missed warranted question fails the case even if the pick
happens to equal the labelled target. Missing bindings fail the case and are
counted separately; they cannot become correct `none` picks.

Decider rows score individual requests: coverage is decisions over labelled
requests, and pass rate is that decider's precision under the same scoring
rule. `missing` identifies requests with no decision. Random and hard sets are
never pooled. A runtime failure excludes the whole case from both baselines,
so a partially completed pair cannot change one baseline's denominator.
Ordinary baseline mistakes are measured results and do not make the command
fail; exclusions exit 1, usage errors 64 and output errors 74.

## What this means for the design

- **Rules only for explicit facts.** A platform reply, a card or thread, an exact name or id, and an offered option chosen by number or label bind by rule, as products do. Word lists, timing and arrival during a run do not. A rule decides the item and not what the message does to it, because three of the four rule misses here were new work under the item it found. A name binds only an open item.
- **Time is a prior.** The focus is the default for a fresh exchange and one candidate among others after an hour.
- **The model chooses from a short list.** Core ranks the focus, waiting and running items first, then recent activity. Here the right item was always in the top three.
- **"New" needs its own check.** New work was 10 percent of messages and the class models detect worst, so a new item is compared with the open items before it is created.
- **Show and move instead of asking.** No fixture needed a question, the current agents asked once needlessly, and the owner corrects agents that ask instead of acting. A question stays for a tie that a move cannot undo.
- **Every input gets a recorded target.** Half of the agents' wrong picks were lost or re-posted messages. A binding for every input makes a dropped one visible.
- **Several requests in one message** (12 percent) get one binding each.

The design is [What a message is about](../design/conversation-and-memory.md#what-a-message-is-about) and [Messages about work](../design/work-and-delegation.md#messages-about-work). Issues #785 to #788 implement it, the replay first.

## Limits

- The data covers 13 days and one person, typing mostly Korean to agents that keep one conversation each. LINA's agents keep threads and run work in the background, so steers to background work will be more frequent than here.
- Labels are model-assisted and judged from a short window. The rate of wrong picks is a lower bound.
- The agents had no registry of 일 and no structured work list, so named and pronoun cues were judged without one. Rules that need a registry are estimated from fixtures, not observed.
- The investing and legal roles had seven messages between them.

## Sources

Products, documentation read on 2026-10-23:

- RFC 5322, section 3.6.4: https://www.rfc-editor.org/rfc/rfc5322#section-3.6.4 ; RFC 5256: https://www.rfc-editor.org/rfc/rfc5256
- Gmail threading: https://support.google.com/mail/answer/5900 ; 2019 change: https://workspaceupdates.googleblog.com/2019/03/threading-changes-in-gmail-conversation-view.html
- Slack threads: https://docs.slack.dev/messaging/retrieving-messages ; Telegram replies: https://core.telegram.org/bots/api#replyparameters ; Discord threads: https://docs.discord.com/developers/topics/threads
- Codex follow-ups: https://learn.chatgpt.com/docs/codex-manual.md ; Devin: https://docs.devin.ai/desktop/devin ; Copilot coding agent: https://docs.github.com/en/copilot/concepts/agents/coding-agent/about-coding-agent and https://github.blog/changelog/2025-10-28-a-mission-control-to-assign-steer-and-track-copilot-coding-agent-tasks
- Todoist quick add: https://www.todoist.com/help/articles/use-task-quick-add-in-todoist-va4Lhpzz
- Google continued conversation: https://support.google.com/assistant/answer/9249169 ; Alexa context carryover: https://www.amazon.science/blog/how-alexa-is-learning-to-converse-more-naturally ; Alexa clarification: https://www.amazon.science/blog/reducing-unnecessary-clarification-questions-from-voice-agents
- Claude projects: https://support.claude.com/en/articles/9519177-how-can-i-create-and-manage-projects

Papers:

- Kummerfeld et al. 2019, A large-scale corpus for conversation disentanglement: https://arxiv.org/abs/1810.11118
- Ma et al. 2022, Structural characterization for dialogue disentanglement: https://arxiv.org/abs/2110.08018
- Lee 2015, argument ellipsis in Korean casual conversation, Linguistic Research 32(3): http://isli.khu.ac.kr/journal/content/data/32_3/9.pdf
- Elgohary et al. 2019, CANARD: https://aclanthology.org/D19-1605.pdf
- Dalton et al. 2020, TREC CAsT 2019 overview: https://arxiv.org/abs/2003.13624
- Li et al. 2024, FnCTOD: https://arxiv.org/abs/2402.10466
- Larson et al. 2019, CLINC150: https://aclanthology.org/D19-1131.pdf
- Arora et al. 2024, Intent detection in the age of LLMs: https://arxiv.org/abs/2410.01627
- Kim et al. 2021, Deciding whether to ask clarifying questions: https://arxiv.org/abs/2109.12451
- Aliannejadi et al. 2019, Qulac: https://arxiv.org/abs/1907.06554
- Zhang et al. 2024, CLAMBER: https://arxiv.org/abs/2405.12063
- Vijayvargiya et al. 2026, Ambig-SWE: https://arxiv.org/abs/2502.13069
- Xiong et al. 2024, Can LLMs express their uncertainty: https://arxiv.org/abs/2306.13063
