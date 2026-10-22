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
