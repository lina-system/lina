# Replay

Recorded-turn replay borrows Core's source readers and runs the kit loop in a
scratch sandbox. Tool effects are answered only by matching recorded results;
local read bindings are supplied explicitly by Core. Native runtime bridge
calls return to the same recorded-result admission inside the sandbox.

Provider replay uses that worker with no live read bindings for synthetic
fixtures. Core owns request assembly, capability selection, the account route,
and stream timing. The CLI reads fixture/episode files and writes private
measurement reports outside Git worktrees. See the
[public fixture and command examples](../../../testdata/provider-replay/README.md).

`ProviderReport` version 1 contains a source digest, route/model identity and
one row per adapter stream. `input` is total input; `plain` is ordinary-rate
input. Responses supplies total input, from which known cache reads are
subtracted. Claude supplies ordinary-rate input, so total input is known only
when read and write counts are also known. Lifetime maps preserve absent
buckets and null counts without guessing. Unknown routes leave plain input
unknown. Failure labels never include provider error bodies.

`Compare` aligns `(turn, round)` across reports with the same source digest;
missing requests remain visible. The CLI rejects duplicate episode IDs and
limits each operation's input size. Core limits replay to 16 adapter requests
per turn and a bounded operation lifetime. All output is separate from canon,
the effect ledger and budget accounting.
