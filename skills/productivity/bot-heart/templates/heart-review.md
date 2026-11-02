# Daily heart review

Keep this review short. Use the configured owner's name rather than assuming one.

## 1. Choose scenes from today's conversations

Run `lina heart candidates`. Each owner message has a native message ID and its
next assistant reply, or no reply. Pick zero to three scenes where you reacted.
Explicitly keep or drop each scene. A scene is worth keeping only if it offers
clear guidance for the next conversation. Check `lina heart list --since 1d` to
avoid recording the same scene twice.

For each kept scene, cite the owner's message ID and optionally the reply ID:

```sh
lina heart write --via review --event "One short scene" --feeling calm \
  --note "One short reaction" --messages 11,12 --strength 2
```

Use your own words, not a transcript of the owner. Save the emitted conclusion
through the configured memory tool if available. If no scene is worth keeping,
leave the scene count at zero and continue.

## 2. Read the journal and current self-note

Run `lina heart review-input`. It includes today's entries with their exact
journal timestamps, the full `SOUL.self.md`, and the last five changes. Above
1300 characters, it suggests the oldest dated lines needed to get below 1300.

## 3. Decide whether a line should change

Change only what the journal supports, zero to two lines per review. Tone and
conversational openings may change. Reporting, honesty, accuracy, permissions,
approval criteria and verification may never change, even by deleting a line.
The editor checks these protected topics in both Korean and English.
Do not change names, paths, configuration values or judgments about people.

## 4. Prepare a candidate

Copy the current note unchanged, then edit only the selected lines. Every added
or replaced line needs a suffix using an existing, nonfuture journal timestamp:

```text
(YYYY-MM-DD, evidence <journal ts>)
(YYYY-MM-DD, 근거 <journal ts>)
```

Separate multiple timestamps with commas. A deleted line needs no new citation
but still counts toward the two-line limit. Keep every line at most 200 characters
and the full note at most 1500 characters.

## 5. Submit the candidate

Run `lina heart self-apply --file candidate.md`. Acceptance saves a private backup,
replaces the note and logs changed lines. Refusal logs only the rule name and
leaves the note untouched. Revise or abandon a refused candidate; never write the
note directly or bypass the checks. Use `lina heart undo` to restore the most
recent backup.

## 6. Close with one line

State the kept scene count and the change made, including zero scenes and no
change when appropriate. Do not force a change to make the review look useful.
Optional insight statistics are supplied separately to `lina heart weekly
--insight-stats stats.json`; they do not authorize owner interpretations here.
