# Narrow terminal behaviour reference

This is a behavioural adaptation; no upstream implementation is copied.

- **Seen:** Hermes Agent (`NousResearch/hermes-agent`, MIT), revision
  `e06df9c2`, `ui-tui/src/components/appChrome.tsx`:
  `busyIndicatorWidth` reserves cells for the glyph, word and elapsed time;
  `statusRuleWidths` makes optional right-hand content yield first;
  the status bar admits complete tail segments in priority order using
  terminal display width. A segment that does not fit is omitted whole.
  The supplied local reference directory was unavailable; these files were
  inspected read-only through the upstream repository at the pinned revision.
- **Inferred:** LINA can measure its localized activity before assigning rows,
  retain the state word and elapsed time, and omit whole footer hint segments.
  Its planner must budget a wrapped activity before allocating transcript space.
- **Unknown:** Hermes does not establish LINA's 80/50-column thresholds,
  four-row transcript floor, Core protocol or interrupt semantics. Issue #455
  supplies the thresholds; LINA's planner and installed-Core PTY tests must
  establish the remaining behaviour. No upstream runtime is executed.

Below 80 columns, activity details move together to a second row only when the
whole line does not fit. Below 50, the hint is shortened to `Esc`. Footer hints
keep their existing order and are included only as whole segments. Measurements
use LINA's Unicode cell-width implementation, including Korean text.
