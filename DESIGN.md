# LINA design

Quiet surface, sharp underneath. LINA is neat and calm: paper, ink and charcoal,
with one small flash of cobalt. The character's blue eyes, thin round glasses
and small dot under one eye inform the identity without turning every screen
into an illustration. Apple's apps and Things 3 are references for restraint,
spacing and care, translated into terminal cells.

The mark is the lowercase wordmark **lina** with a small accent dot: `lina·`.
The dot is ordinary Unicode, with `lina.` as its ASCII form. The app uses the
same wordmark and an [SVG circle](design/tokens/mark.svg), not a font icon. It remains recognizable in
monochrome. This decides the brand mark, not the persona's facial expressions.

## Use it

- Let the person's content lead. Use generous margins, consistent alignment and
  empty rows between groups. Prefer a hairline to a box.
- Keep the wordmark small and firm. Give secondary text enough contrast to read.
- Use cobalt only for the mark, primary action, selection and Waiting for me.
  Use neutral marks and words for other states.
- Give every state a distinct mark and a word. Keep the same information in
  ASCII and `NO_COLOR`. Never depend on animation to explain progress.
- Use the semantic tokens. Change their source, regenerate, and inspect the result.

Do not fill panels with the accent, add rainbow status colours, use heavy boxes,
put private-use font glyphs in the terminal, or use faint text as a substitute
for hierarchy. Avoid decorative animation and repeated branding.

## Tokens and terminal profiles

The three DTCG files in [design/tokens](design/tokens/) are the source of truth.
`node scripts/tokens.mjs` generates the Go data, CSS custom properties, SVG mark and the
following tables. `node scripts/tokens.mjs --check` compares without writing,
checks text contrast, and rejects raw presentation colours and glyphs in TUI
production source. Foundation runs the same check.

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
| color.accent | `#2F4BDB` (26 / 4) | `#91A7FF` (111 / 15) | `#ADC0FF` (153 / 15) |
| color.canvas | `#F8F9FC` (231 / 15) | `#17191F` (234 / 0) | `#000000` (16 / 0) |
| color.line | `#CFD3DE` (252 / 7) | `#383E4C` (238 / 8) | `#FFFFFF` (231 / 15) |
| color.muted | `#626978` (241 / 0) | `#A6ADBD` (249 / 7) | `#DDDDDD` (253 / 7) |
| color.surface | `#EEF0F6` (255 / 7) | `#20232B` (235 / 0) | `#000000` (16 / 0) |
| color.text | `#20232D` (234 / 0) | `#F4F5F9` (255 / 15) | `#FFFFFF` (231 / 15) |
| glyph.ascii.action | `">"` | `">"` | `">"` |
| glyph.ascii.connecting | `"~"` | `"~"` | `"~"` |
| glyph.ascii.failed | `"x"` | `"x"` | `"x"` |
| glyph.ascii.live | `"*"` | `"*"` | `"*"` |
| glyph.ascii.mark | `"."` | `"."` | `"."` |
| glyph.ascii.quit | `"+"` | `"+"` | `"+"` |
| glyph.ascii.recovering | `":"` | `":"` | `":"` |
| glyph.ascii.refused | `"!"` | `"!"` | `"!"` |
| glyph.ascii.resyncing | `"="` | `"="` | `"="` |
| glyph.ascii.retrying | `">"` | `">"` | `">"` |
| glyph.ascii.rule | `"-"` | `"-"` | `"-"` |
| glyph.ascii.stopped | `"o"` | `"o"` | `"o"` |
| glyph.ascii.unknown | `"?"` | `"?"` | `"?"` |
| glyph.ascii.waiting | `"?"` | `"?"` | `"?"` |
| glyph.unicode.action | `"›"` | `"›"` | `"›"` |
| glyph.unicode.connecting | `"◌"` | `"◌"` | `"◌"` |
| glyph.unicode.failed | `"×"` | `"×"` | `"×"` |
| glyph.unicode.live | `"●"` | `"●"` | `"●"` |
| glyph.unicode.mark | `"·"` | `"·"` | `"·"` |
| glyph.unicode.quit | `"✓"` | `"✓"` | `"✓"` |
| glyph.unicode.recovering | `"◐"` | `"◐"` | `"◐"` |
| glyph.unicode.refused | `"⊘"` | `"⊘"` | `"⊘"` |
| glyph.unicode.resyncing | `"↻"` | `"↻"` | `"↻"` |
| glyph.unicode.retrying | `"↗"` | `"↗"` | `"↗"` |
| glyph.unicode.rule | `"─"` | `"─"` | `"─"` |
| glyph.unicode.stopped | `"○"` | `"○"` | `"○"` |
| glyph.unicode.unknown | `"?"` | `"?"` | `"?"` |
| glyph.unicode.waiting | `"?"` | `"?"` | `"?"` |
| icon.size | `20px` | `20px` | `20px` |
| motion.reduced | `0ms` | `0ms` | `0ms` |
| motion.settle | `160ms` | `160ms` | `160ms` |
| radius.card | `12px` | `12px` | `12px` |
| space.gutter | `24px` (3 cells) | `24px` (3 cells) | `24px` (3 cells) |
| space.inline | `8px` (1 cells) | `8px` (1 cells) | `8px` (1 cells) |
| space.page | `32px` (2 cells) | `32px` (2 cells) | `32px` (2 cells) |
| space.section | `24px` (1 cells) | `24px` (1 cells) | `24px` (1 cells) |
| type.body | `"Pretendard", "sans-serif"` | `"Pretendard", "sans-serif"` | `"Pretendard", "sans-serif"` |
| type.code | `"ui-monospace", "monospace"` | `"ui-monospace", "monospace"` | `"ui-monospace", "monospace"` |
| type.lineHeight | `1.5` | `1.5` | `1.5` |
| type.size | `16px` | `16px` | `16px` |
| type.titleWeight | `600` | `600` | `600` |
| type.weight | `400` | `400` | `400` |

<!-- tokens:end -->
