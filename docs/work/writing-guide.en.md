# How to write what the dashboard shows (every agent)

<!-- Generated with `lina work fields --markdown > docs/work/writing-guide.en.md`. Don't edit by hand; edit hermes_cli/work/writing_fields.py instead. -->

**The reader is the owner.** Write as if they see it for the first time on a summary screen, without the context of the work. Do not carry over working words (files, commands, devices, issue numbers, internal tool names). Write like a task in a work tracker (Linear, Jira).

## Common
- Use plain language. One fact per field.
- **Leave out:**
  - issue and PR numbers (`#19`)
  - task ids (`OT-…`, `T…`, `G…`)
  - device names (`devices.workers`)
  - commands and options (`lina worker send`, `--omo`)
  - files, paths, commits
  - internal tool names: say `dashboard`, `memory compression`, `job`
- Product and service names are allowed. e.g. GitHub, Claude, OpenAI, Gemini, Slack, Telegram, NAS.
- Don't add extras in parentheses. Put anything extra in another field.

## Rules per field
| Field | Length | Format | Contents |
|---|---|---|---|
| goal title | 50 chars | a noun phrase naming the result | the result the owner wants, taken from their words |
| done condition | 100 chars | one sentence about a state you can check | what is seen or works when it is done |
| to-do title | 50 chars | object + action (add, fix, tidy, check, research, ship) | what this to-do changes |
| why | 70 chars | one sentence starting with `so` | what gets better for the owner when it is done, readable without the goal |
| progress note | 100 chars | `Did …. Next: ….` | what was done and what comes next |
| decision | 110 chars | a question `…?` or a request `please …` | what has to be decided or done |
| on decision | 70 chars | `once decided, …` | what moves once it is decided |
| answer choice | 30 chars | 2 to 4 answers, one recommended | for questions only |
| wait | 80 chars | `Waiting for …` | what, whom, until when |
| hold reason | 80 chars | reason + what releases it | who does what to release it |
| reply | 100 chars | what was done + what was checked | matches the result chip (applied, won't apply, needs answer) |
| due date | 10 chars | YYYY-MM-DD | when it must be done; only a date from the owner or the request |

## Rewritten example
| Ago | Title | Why |
|---|---|---|
| Order gate #19: recheck breaker, filter secrets | Check the order gate safeguards | so no order goes out without approval |
| Take the LCM fix, deploy to all, measure | Ship the memory compression fix and measure it | so long chats do not slow answers down |
| ledger: comments, replies, done mark | Dashboard comments and done marks | so feedback and done marks happen right on the dashboard |
| lina worker send: answer while it waits | Answer jobs that stop on a question | so a job never sits waiting for a person |
| OT-ABC test job notice check | Check job end notices | so the agent always hears when a job ends |
| dashboard: polish the close-goal box | Polish the goal close dialog | so a goal with open to-dos is not closed by mistake |

## Check
`work` checks each field against the rules below before writing. If rejected, nothing is written. `work lint` shows existing text that breaks the rules, and `work fields` shows a description and example for each field.

| Rejection rule | Matching text (regex, English word boundaries) |
|---|---|
| issue or PR number | `#\d+` |
| task id | `\b(?:OT\|OG\|OS)-[A-Z0-9]+\|\b[TG]\d{2,}\b` |
| command or option | `\blina (?:worker\|work)\b\| --[a-z]` |
| path | `/Users/\|/home/\|~/` |
| internal name | `\bledger\b\|\bomo\b\|\bLCM\b` |
| parentheses | `[()]` |
| leading tag | `^[^\s:]+:\s` (goal name · to-do title) |
| Length | text longer than the limit in the per-field rules table |

| Field | Used in | Required | If it fails | Command |
|---|---|---|---|---|
| goal title | goal | required | refuse | work add goal --title · work edit --title |
| done condition | goal, to-do | required | warn | work add --done · work edit --done |
| to-do title | to-do | required | refuse | work add to-do --title · work edit --title |
| why | to-do | required | refuse | work add to-do --why · work edit --why |
| progress note | note | optional | warn | work note · work add --note |
| decision | decision | optional | refuse | work wait --decide · work edit --decide |
| on decision | decision | optional | refuse | the dashboard writes it · work plain |
| answer choice | decision | optional | refuse | work wait --choice |
| wait | wait | required when waiting | refuse | work wait --on --next |
| hold reason | wait | optional | refuse | work wait --hold --on |
| reply | reply | optional | warn | work reply |
| due date | goal, to-do | optional | refuse | work add --due · work edit --due |

## Examples for each field

### goal title
Example source: guide

- Good example: Dashboard comments and done marks
- Good example: Ship the memory compression fix and measure it
- Bad example: ledger: comments, replies, done mark — internal name, leading tag
- Bad example: Take the LCM fix, deploy to all, measure — internal name

### done condition
Example source: made up

- Good example: No order goes out without approval, even with live orders on
- Good example: A dashboard comment reaches the agent
- Bad example: breaker verdict recheck done — 
- Bad example: #19 closed — issue or PR number

### to-do title
Example source: guide

- Good example: Check the order gate safeguards
- Good example: Check job end notices
- Bad example: Order gate #19: recheck breaker, filter secrets — issue or PR number
- Bad example: OT-ABC test job notice check — task id

### why
Example source: guide · made up

- Good example: so no order goes out without approval
- Good example: so the agent always hears when a job ends
- Bad example: to recheck the breaker before live orders (#19) — issue or PR number, parentheses
- Bad example: so omo notices arrive — internal name

### progress note
Example source: made up

- Good example: Ran the safeguard test. Next: check the live order settings.
- Good example: Fixed two missed notices. Next: watch a day.
- Bad example: ran lina worker send — command or option
- Bad example: merged PR #19 (CI green) — issue or PR number, parentheses

### decision
Example source: made up

- Good example: Keep the order safeguard limits as they are?
- Good example: Please approve the order once in the broker app
- Bad example: keep breaker limit (3)? — parentheses
- Bad example: turn on --omo after OT-ABC1101? — task id, command or option, internal name

### on decision
Example source: made up

- Good example: Once decided, the live order settings change
- Good example: Once decided, the remaining to-dos close
- Bad example: once decided, restart in ~/bin (manual) — path, parentheses
- Bad example: once decided, go on with #21 — issue or PR number

### answer choice
Example source: made up

- Good example: Keep as is
- Good example: Postpone a week
- Bad example: keep (recommended) — parentheses
- Bad example: decide after OT-ABC — task id

### wait
Example source: made up

- Good example: Waiting for the build machine reboot tonight
- Good example: Waiting for the sign-up mail by Friday
- Bad example: waiting for lina worker reboot — command or option
- Bad example: waiting for ~/Desk sync — path

### hold reason
Example source: made up

- Good example: Stopped: no order limit. Released when the owner sets one
- Good example: Stopped: the NAS is off. Released when it is back
- Bad example: /home disk down — 
- Bad example: LCM error (cause unknown) — internal name, parentheses

### reply
Example source: made up

- Good example: Fixed the notice text. Saw the test notice reach the agent.
- Good example: Postponed as asked. Will ask again Monday.
- Bad example: fixed in PR #19 — issue or PR number
- Bad example: ledger fix (commit abc123) — internal name, parentheses

### due date
Example source: made up

- Good example: 2026-11-05
- Good example: 2026-11-30
- Bad example: 11-05 — date shape
- Bad example: next Friday — Over 10 characters, date shape
