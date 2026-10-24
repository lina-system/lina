# Sender identity evaluation set

`synthetic.json` is the labelled set `lina identity-eval` runs by default. It is
frozen: the matcher is measured against it again after every change, so edit it
as a deliberate change with the expected results.

The set holds known people with their aliases and contacts, the identities a
source saw with the identifiers each showed, and the ambiguities planted on
purpose. `expect` is the people record a usable link must name, `unknown` where
no usable link is correct, and `self` for the person's own account, which is
never linked as another person. Every planted ambiguity must make a link
unusable and ask the person once.

`at` is the time a source showed the identity. `observed_at` is when the matcher
saw it, for evidence that arrives out of order: a backfill carries an earlier
`at` and a later `observed_at`. Identities are observed in `observed_at` order,
and the measures are taken after the last one.

A `windows` entry names a period in which a link to a record must not be usable,
with `from`, `until` and a one-line `why`. It is how the set pins the end of a
link whose identifier moved: the end is the earliest sighting of the move, so a
backfilled earlier sighting moves it back and never later.

| Hard case from the research note | Where it is in the set |
| --- | --- |
| the same name for different people | `d-sam` against `p-sam-a` and `p-sam-b` |
| one person under different names | `d-hana` ("H. Lee") links by phone to `p-hana` |
| nickname changes over time | `d-ben` ("Ben", then "Benji") proposes `p-ben` |
| a family phone shared by two people | `imessage//+821055550001` and `imessage//+821055550002` |
| a recycled number | `d-old`, then `whatsapp//+821066660001` |
| a move reported out of order | `discord//d-moved`, then `imessage//+821066660002` in March, then its February backfill; the `windows` entry pins the end at February |
| a team mailbox | `slack/T-team/U-team` against `p-team-a` and `p-team-b` |
| a contact card listing two people's numbers | `d-card` against `p-card-a` and `p-card-b` |
| Slack ids in two workspaces | `slack/T-anna/U-anna` and `slack/T-other/U-anna` |
| the person's own second account | `imessage//+821099990001` with `self` |

Phones are written as a source shows them on purpose; the matcher normalizes to
E.164 and lower-case email. Emails are written in mixed case for the same
reason.

## Running

```
lina identity-eval
```

The command prints the measures and exits non-zero when the gate fails. It runs
on a temporary store, so nothing in the installation is read or changed.

## The owner's sample

The owner's labelled sample never enters the repository. On the owner's machine
the private run reads a directory with three files and writes counts only:

- `people.json`: `[{ "id", "names", "contacts": [{"kind","value","source"}] }]`
- `identities.json`: `[{ "platform", "scope", "user", "at", "self", "names", "identifiers" }]`
- `labels.json`: `{ "<platform>/<scope>/<user>": "<record id>" | "unknown" | "self" }`

```
lina identity-eval --private <dir> [--counts <file>]
```

`counts.json` carries counts, rates and the rule-of-three bound, never a name, an
id or a text. Learning about other people stays off until at least 300 usable
links are checked with none wrong.
