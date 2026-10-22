# Terminal UI behaviour references

## Complete assistant replies (#445)

Reference: [Hermes Agent](https://github.com/NousResearch/hermes-agent), MIT,
revision `e06df9c2`, as recorded in the 2026-10-22 terminal UI source survey.
This is a behaviour reference; no upstream code is copied.

- **Seen (source survey):** `ui-tui/src/components/messageLine.tsx:211-213`
  renders completed assistant prose with the full Markdown component.
  `useInputHandlers.ts:622-640` pages the transcript by half a viewport.
  `lib/text.ts:107-130` bounds only live prose, with a visible live-tail label;
  completed prose is not folded behind a key.
- **Inferred:** a completed long answer should remain one scrollable conversation
  item. LINA can display the already masked generated reply card's text in that
  position, retaining an optional pager affordance after the text. Structured
  result cards keep their separate summary-and-detail presentation.
- **Unknown:** Korean terminal rendering and restart recovery were not exercised
  in the reference survey. LINA verifies those behaviours with its own installed
  Core and real PTY acceptance test. Native terminal scrollback and mouse input
  are outside this change.

## Conversation wheel and sticky viewport (#448)

Reference: Hermes Agent, MIT, revision `e06df9c2`, read directly from the
upstream source because the suggested local reference checkout was unavailable.
No upstream code is copied.

- **Seen:** `ui-tui/src/app/useInputHandlers.ts:593-635` dispatches wheel
  directions, uses inter-event timing for acceleration and resets on direction
  changes; Page Up/Down move half a viewport.
  `ui-tui/packages/hermes-ink/src/ink/components/ScrollBox.tsx:174-235` breaks
  bottom stickiness on manual scrolling and restores it on `scrollToBottom`.
  `ui-tui/src/app/scroll.ts` clamps the accepted scroll target before rendering.
- **Inferred:** LINA should retain the first visible block and its row while
  new output arrives, follow new output at the bottom, and expose End to resume
  following. Its own wheel policy starts at three rows, adds three for each
  same-direction repeat within 120 ms, and caps at twelve; idle or a direction
  change resets the step. These constants are LINA choices, not upstream code.
- **Unknown:** the reference's native selection modifier, terminal mode cleanup
  and installed-Core recovery were not exercised. LINA uses DEC 1000 plus SGR
  1006, ignores button events and never enables motion modes 1002/1003. Native
  selection uses the terminal's override (Shift+drag in xterm/GNOME Terminal;
  Option+drag in iTerm2). LINA verifies its own modes, decoder and viewport with
  synthetic input and an installed Core PTY test.
