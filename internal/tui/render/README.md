# Incremental text rendering

`Stream` owns one item's sanitized text and an index of wrapped rows. `Append`
ignores replayed revisions and revisits only the partial row and its boundary
row. Complete rows retain their revisions. Blank lines close paragraphs, lists
and tables; a matching fence closes a fenced block; headings close at newline.
An open fence stays plain. `Seal` replaces the item with authoritative text and
ignores replayed completions and subsequent deltas.

`Renderer.Draw` takes an indexed `Source`, a viewport and a configuration. It
reads at most the viewport plus overscan on either side, keeps only those cache
entries, and returns changed rows. Its trusted painter receives sanitized,
clipped, closed rows; an open row stays plain. A caller applies the returned row
replacements to its terminal adapter, including empty replacements that clear
old rows. An empty change list produces no terminal output. Styling and terminal
I/O belong to that adapter. Render workers use the reducer generation to reject
stale results before applying patches.

For resize, retain the item's id and a `Row.Offset` anchor, then call
`Stream.Window` with the new width and required row count including overscan.
It stops measuring at the end of that window; it does not reflow the history.
`Rows` adapts its result to `Source`. The UI owns resize coalescing and worker
scheduling. The conversation uses `Stream.Markdown` for semantic Markdown rows.

`Measurer` serves wrapping, truncation and cursor placement. The default
`Unicode` implementation uses per-codepoint cell widths; select `Graphemes`
only after the terminal confirms mode 2027. Both modes keep ambiguous width
narrow and Hangul syllables two cells. Wrapping never splits an extended grapheme
cluster. A cluster wider than the entire row is omitted with `Row.Clipped` set.
`Cursor` consumes sanitized text and a UTF-8 byte offset, clamping offsets inside
a cluster to its start. This is cell arithmetic, not evidence about a real IME.

The segmentation algorithm implements Unicode 17 UAX #29, with the upstream
conformance corpus in `testdata/GraphemeBreakTest.txt`. Range tables are Unicode
data, not an imported implementation; their notice is in
[THIRD-PARTY-NOTICES.md](../../../THIRD-PARTY-NOTICES.md). The implementation
uses only the standard library and the sibling sanitizer. The UI supplies the
Markdown parser through a function boundary; no external parser or Charm package
enters this layer.

To reproduce the tables, download these Unicode 17.0.0 files into one directory:

- [GraphemeBreakProperty.txt](https://www.unicode.org/Public/17.0.0/ucd/auxiliary/GraphemeBreakProperty.txt)
- [EastAsianWidth.txt](https://www.unicode.org/Public/17.0.0/ucd/EastAsianWidth.txt)
- [emoji-data.txt](https://www.unicode.org/Public/17.0.0/ucd/emoji/emoji-data.txt)
- [DerivedCoreProperties.txt](https://www.unicode.org/Public/17.0.0/ucd/DerivedCoreProperties.txt)

From the repository root, run:

```sh
go run ./internal/tui/render/internal/unicodegen <download-directory> > unicode_tables.go
```

The generator rejects input with a different SHA-256 digest. Compare the output
with `internal/tui/render/unicode_tables.go` before replacing that file. Ordinary
tests are offline and verify the pinned grapheme conformance corpus as well as
Hangul, combining, emoji, cursor, clipping and streaming properties.

`Stream.Markdown` projects closed blocks into semantic spans for emphasis, code,
lists and links. It wraps visible text with the same Unicode measurer, then the
UI paints only visible rows using generated theme tokens. Closed prefixes retain
their parsed spans and wrapped rows across appends. The open tail consumes new
bytes once, withholding pending Markdown delimiters and link destinations; it
keeps literal text inside code. `Seal` replaces that preview with the
CommonMark parse of the authoritative text, including reference definitions.
Malformed or escaped syntax in a sealed item remains literal. ANSI and OSC are
never admitted as source styling, including after Markdown entity decoding.

Closed pipe tables arrive as `MarkdownTable` cells and column alignments from
the UI's pinned Goldmark table extension. `MarkdownTable.Rows` measures the
visible cell text with the shared `Measurer`, pads columns, and clips complete
rows at the viewport edge without wrapping. It retains inline styles and clips
joined spans at grapheme boundaries. The cached semantic table reflows on width
or width-mode changes; the open preview follows the existing blank-line/Seal
boundary. Unit tests cover 40/80/140 columns in both modes, escaped pipes,
alignment, styles, clipping and every streaming byte split. The
`TestM1TUIMarkdownTables` acceptance scenario exercises the built TUI through a
real PTY with a fake Core held at open, closed and completed reply checkpoints.
