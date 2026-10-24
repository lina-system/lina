# LINA design

Quiet surface, sharp underneath. LINA is neat and calm: paper, ink and charcoal,
with one small flash of cobalt. The character's blue eyes, thin round glasses
and small dot under one eye inform the identity without turning every screen
into an illustration. Apple's apps and Things 3 are references for restraint,
spacing and care, translated into terminal cells.

The mark is the lowercase wordmark **lina** with a small baseline accent dot: `lina.`.
The terminal uses the same baseline period in Unicode and ASCII. The app uses the
same wordmark and an [SVG circle](design/tokens/mark.svg), not a font icon. It remains recognizable in
monochrome. This decides the brand mark, not the persona's facial expressions.

## Use it

- Let the person's content lead. Use generous margins, consistent alignment and
  empty rows between groups. Prefer a hairline to a box.
- Keep the wordmark bold and bright; state labels use regular text. Place its
  cobalt dot on the baseline. Give secondary text enough contrast to read.
- Use saturated cobalt for the mark, active state glyph, key, primary action,
  selection and Waiting for me. Keep action labels and inactive state glyphs
  secondary; state words remain readable without colour.
- Give every state a distinct mark and a word. Keep the same information in
  ASCII and `NO_COLOR`. Never depend on animation to explain progress.
- Give conversation turns a first-line speaker gutter, align continuation text,
  and leave one empty row between turns; keep speaker words in ASCII and monochrome.
- Use the semantic tokens. Change their source, regenerate, and inspect the result.

Do not fill panels with the accent, add rainbow status colours, use heavy boxes,
put private-use font glyphs in the terminal, or use faint text as a substitute
for hierarchy. Avoid decorative animation and repeated branding.

## Connection surface

The wordmark, state and optional footer share one reading edge and one blank row
between groups, inside the two-row page margin. There is no divider. The state
keeps its distinct glyph and word in every profile; connected uses the filled
active dot, while connecting uses the open dotted ring. Error and exit marks are
neutral. A supporting line must add information; connected needs no second line.
The footer shows `[q] Quit`: secondary brackets and label, with only the key in
cobalt. `q`, Ctrl+C and Ctrl+D close the connection without Enter and restore the
terminal's original mode. There is at most one action on this connection screen.
Connection failures appear once in the panel; redirected stderr and plain output
keep their command diagnostics.

## Tokens and terminal profiles

The three DTCG files in [design/tokens](design/tokens/) are the source of truth.
`node scripts/tokens.mjs` generates the Go data, CSS custom properties, the
launcher panel's Swift constants, SVG mark and the following tables.
`node scripts/tokens.mjs --check` compares without writing,
checks text contrast, and rejects raw presentation colours and glyphs in TUI
production source. Foundation runs the same check.

The launcher's Swift file holds the six colors as dynamic `NSColor` values that
pick light, dark or high contrast from the panel's appearance, plus spacing,
radius and motion constants. Glass, materials and corner shape stay the
system's. The high-contrast set is dark, reaches 7:1 against canvas and surface,
and applies in either appearance while Increase Contrast is on. Spacing, radius
and motion are the same in every theme, and the loader rejects a theme where
they differ, because the Swift file holds one value per role.

All text roles (text, muted and accent) must reach 4.5:1 on both canvas and
surface, including the standard 16-colour and 256-colour fallback palettes.
The separator is decorative and does not carry information. Actual ANSI palettes
are user configurable; their physical colours cannot be guaranteed by the app.
On dark 16-colour terminals the active accent becomes bright white: readable
emphasis takes priority when a cobalt-like ANSI slot cannot meet contrast.

`LINA_THEME=light|dark|high-contrast` selects a theme; otherwise `COLORFGBG` selects
light or dark (dark by default). `LINA_GLYPHS=ascii` selects ASCII marks. Terminal
capabilities choose truecolour, 256 or 16 colours. A nonempty `NO_COLOR` removes
all escape styling but preserves the layout and state labels. Pipes and terminals
without a usable `TERM` retain the plain connection-code interface. These are
process-local display overrides; no preferences are saved by this increment.
Omarchy theme-file discovery remains part of platform preferences work.

<!-- tokens:start -->
Generated values; terminal indexes are 256 / 16. Spacing cells are independent
of app pixels. Duration values are milliseconds.

| Role | Light | Dark | High contrast |
| --- | --- | --- | --- |
| color.accent | `#2F4BDB` (26 / 4) | `#5685FF` (69 / 15) | `#6795FF` (69 / 15) |
| color.canvas | `#F8F9FC` (231 / 15) | `#17191F` (234 / 0) | `#000000` (16 / 0) |
| color.line | `#CFD3DE` (252 / 7) | `#383E4C` (238 / 8) | `#FFFFFF` (231 / 15) |
| color.muted | `#626978` (241 / 0) | `#A6ADBD` (249 / 7) | `#DDDDDD` (253 / 7) |
| color.surface | `#EEF0F6` (255 / 7) | `#20232B` (235 / 0) | `#000000` (16 / 0) |
| color.text | `#20232D` (234 / 0) | `#F4F5F9` (255 / 15) | `#FFFFFF` (231 / 15) |
| glyph.ascii.action | `">"` | `">"` | `">"` |
| glyph.ascii.activityFrames | `"|/-\\"` | `"|/-\\"` | `"|/-\\"` |
| glyph.ascii.activitySeparator | `"-"` | `"-"` | `"-"` |
| glyph.ascii.agent | `"*"` | `"*"` | `"*"` |
| glyph.ascii.back | `"<"` | `"<"` | `"<"` |
| glyph.ascii.connecting | `"~"` | `"~"` | `"~"` |
| glyph.ascii.failed | `"x"` | `"x"` | `"x"` |
| glyph.ascii.live | `"*"` | `"*"` | `"*"` |
| glyph.ascii.mark | `"."` | `"."` | `"."` |
| glyph.ascii.overflow | `"..."` | `"..."` | `"..."` |
| glyph.ascii.person | `">"` | `">"` | `">"` |
| glyph.ascii.quit | `"+"` | `"+"` | `"+"` |
| glyph.ascii.recovering | `":"` | `":"` | `":"` |
| glyph.ascii.refused | `"!"` | `"!"` | `"!"` |
| glyph.ascii.resyncing | `"="` | `"="` | `"="` |
| glyph.ascii.retrying | `">"` | `">"` | `">"` |
| glyph.ascii.rule | `"-"` | `"-"` | `"-"` |
| glyph.ascii.stageDone | `"+"` | `"+"` | `"+"` |
| glyph.ascii.stageFailed | `"x"` | `"x"` | `"x"` |
| glyph.ascii.stageNeedsInput | `"!"` | `"!"` | `"!"` |
| glyph.ascii.stageNext | `"."` | `"."` | `"."` |
| glyph.ascii.stageWaiting | `"o"` | `"o"` | `"o"` |
| glyph.ascii.stageWorking | `"*"` | `"*"` | `"*"` |
| glyph.ascii.stopped | `"o"` | `"o"` | `"o"` |
| glyph.ascii.toolFailed | `"x"` | `"x"` | `"x"` |
| glyph.ascii.toolOk | `"+"` | `"+"` | `"+"` |
| glyph.ascii.toolRunning | `"~"` | `"~"` | `"~"` |
| glyph.ascii.unknown | `"?"` | `"?"` | `"?"` |
| glyph.ascii.waiting | `"?"` | `"?"` | `"?"` |
| glyph.unicode.action | `"›"` | `"›"` | `"›"` |
| glyph.unicode.activityFrames | `"◐◓◑◒"` | `"◐◓◑◒"` | `"◐◓◑◒"` |
| glyph.unicode.activitySeparator | `"·"` | `"·"` | `"·"` |
| glyph.unicode.agent | `"●"` | `"●"` | `"●"` |
| glyph.unicode.back | `"‹"` | `"‹"` | `"‹"` |
| glyph.unicode.connecting | `"◌"` | `"◌"` | `"◌"` |
| glyph.unicode.failed | `"×"` | `"×"` | `"×"` |
| glyph.unicode.live | `"●"` | `"●"` | `"●"` |
| glyph.unicode.mark | `"."` | `"."` | `"."` |
| glyph.unicode.overflow | `"…"` | `"…"` | `"…"` |
| glyph.unicode.person | `"›"` | `"›"` | `"›"` |
| glyph.unicode.quit | `"✓"` | `"✓"` | `"✓"` |
| glyph.unicode.recovering | `"◐"` | `"◐"` | `"◐"` |
| glyph.unicode.refused | `"⊘"` | `"⊘"` | `"⊘"` |
| glyph.unicode.resyncing | `"↻"` | `"↻"` | `"↻"` |
| glyph.unicode.retrying | `"↗"` | `"↗"` | `"↗"` |
| glyph.unicode.rule | `"─"` | `"─"` | `"─"` |
| glyph.unicode.stageDone | `"✓"` | `"✓"` | `"✓"` |
| glyph.unicode.stageFailed | `"×"` | `"×"` | `"×"` |
| glyph.unicode.stageNeedsInput | `"!"` | `"!"` | `"!"` |
| glyph.unicode.stageNext | `"·"` | `"·"` | `"·"` |
| glyph.unicode.stageWaiting | `"○"` | `"○"` | `"○"` |
| glyph.unicode.stageWorking | `"●"` | `"●"` | `"●"` |
| glyph.unicode.stopped | `"○"` | `"○"` | `"○"` |
| glyph.unicode.toolFailed | `"×"` | `"×"` | `"×"` |
| glyph.unicode.toolOk | `"✓"` | `"✓"` | `"✓"` |
| glyph.unicode.toolRunning | `"◐"` | `"◐"` | `"◐"` |
| glyph.unicode.unknown | `"?"` | `"?"` | `"?"` |
| glyph.unicode.waiting | `"?"` | `"?"` | `"?"` |
| icon.size | `20px` | `20px` | `20px` |
| motion.activityElapsed | `1000ms` | `1000ms` | `1000ms` |
| motion.activityFrame | `100ms` | `100ms` | `100ms` |
| motion.reduced | `0ms` | `0ms` | `0ms` |
| motion.settle | `160ms` | `160ms` | `160ms` |
| radius.card | `12px` | `12px` | `12px` |
| space.gutter | `24px` (3 cells) | `24px` (3 cells) | `24px` (3 cells) |
| space.inline | `8px` (1 cells) | `8px` (1 cells) | `8px` (1 cells) |
| space.page | `32px` (2 cells) | `32px` (2 cells) | `32px` (2 cells) |
| space.section | `24px` (1 cells) | `24px` (1 cells) | `24px` (1 cells) |
| type.body | `"Pretendard", sans-serif` | `"Pretendard", sans-serif` | `"Pretendard", sans-serif` |
| type.code | `ui-monospace, monospace` | `ui-monospace, monospace` | `ui-monospace, monospace` |
| type.lineHeight | `1.5` | `1.5` | `1.5` |
| type.size | `16px` | `16px` | `16px` |
| type.titleWeight | `700` | `700` | `700` |
| type.weight | `400` | `400` | `400` |

<!-- tokens:end -->
