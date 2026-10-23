# Result bodies and generated cards

Core validates closed typed bodies here and derives their complete text. Surface
wire schemas are in the canonical client protocol schema; `Surface` adds tree,
data binding and action checks without IO or a renderer dependency.

`FoldSurface` copies the previous revision and applies a bounded A2UI v0.9 batch.
Only its validated whole result can be stored. The Core `present_surface` tool
uses the ordinary result authority and ledger transaction. Deleting a surface
stores a tombstone. No schema migration is needed for the existing result ledger.

The choice and progress fixtures reproduce the two card shapes measured in
[the generated UI research](../../docs/research/generated-ui.md). They are
synthetic data, authored here; no upstream implementation or renderer is copied.
The wire shapes were checked against A2UI 0.9.1 at `020a9d6`, using the pinned
`v0.9` wire string. Functions, themes and templates are outside LINA's catalog.
