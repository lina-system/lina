# Work panel event latency

`TestM3WorkPanelEventLatency` runs Core boot with a fixture manager and stub
background workers, the canonical store and normal progress publisher, and
real TUI subprocesses in PTYs. It admits 50 graphs and names from
`work-panel.json`. All 50 remain in progress throughout measurement.

The fan-out graph first releases its root and starts its 82 parallel steps.
Then 50 of those steps produce files and return successful observations to
Core, one at a time. Every landing changes the card’s Now and remaining parallel
count. The other 49 graphs remain live. The fixture never writes progress
payloads or mutates Core’s database from a second writer.

Eight PTYs observe the same 50 commits: English/Korean, Unicode/ASCII, and
120x34 sidebar/100x34 overlay. `panelFrameProbe` arms before each worker result
and retains the first complete renderer write that changes the selected card.
Split reads cannot satisfy it; coalesced frames cannot overwrite the first
match. The frame must agree with Core’s new Now.

Each sample logs the event and step identities, Core’s durable `recorded_at`,
first-frame timestamp, latency, write number and parsed screen. The timestamp
is written within Core’s transaction, so storage time is included. Every
combination independently requires 50 distinct valid samples, nearest-rank
p95 ≤500 ms and maximum ≤1 s. The shared T-16 E8/E9 evaluators also consume the
sidebar recordings. The draft, selected card and scrolled conversation remain
stable; closing each overlay must restore its draft and reading position. The
reading position is the conversation's rows; the running strip under them in
the overlay layout is live status and changes as steps land.

```sh
go test -count=1 -v -run WorkPanel ./e2e/m1/...
```

The focused measurement self-tests cover split/coalesced frames, absent or
duplicate samples, invalid timestamps and exact/over-budget p95/max boundaries.
No failed latency sample is retried or excluded.
