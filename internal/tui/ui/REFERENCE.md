# Terminal UI behaviour references

## Whole-conversation overlay (#458)

Reference: [Codex CLI](https://github.com/openai/codex), Apache-2.0,
revision `b337fe06b7d8d6e5b42295e04c5e92c2b9c46567`. Source read before implementation;
no upstream code is copied.

- **Seen:** `codex-rs/tui/src/pager_overlay.rs` constructs a transcript overlay
  from retained history cells and restores default input when it closes.
  `pager_overlay/transcript.rs` identifies the inline Ctrl+T view and delegates
  scrolling, search and its live tail to the transcript viewport.
- **Inferred:** LINA can expose the retained visible conversation through its
  existing sanitized plain-text pager, preserving speaker words and composer
  state. It should read current display state so replacements and revocations
  also apply while the overlay is open.
- **Unknown:** reference terminal selection, Korean input and clipboard effects
  were not exercised. LINA retains terminal-owned copying without clipboard
  writes and verifies its own installed Core, PTY, search and draft restoration.

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

## Ctrl+C confirmation (#452)

Reference: [Hermes Agent](https://github.com/NousResearch/hermes-agent), MIT,
revision `944c3a434071f43e64b443d033f3eba30c64434c`.
Read before implementing #452; no upstream code is copied.

- **Seen:** [`_tui_handle_ctrl_c` and `_tui_clear_or_exit`](https://github.com/NousResearch/hermes-agent/blob/944c3a434071f43e64b443d033f3eba30c64434c/hermes_cli/cli_tui_mixin.py)
  clear text and attachments first. With a running agent and an empty draft,
  the first press interrupts with a hint and a second within less than two
  seconds exits. An idle empty draft exits immediately. The
  [draft-priority tests](https://github.com/NousResearch/hermes-agent/blob/944c3a434071f43e64b443d033f3eba30c64434c/tests/hermes_cli/test_cli_ctrl_c_draft_priority.py)
  check that clearing a draft does not interrupt the running turn.
- **Inferred:** draft clearing and a bounded second-press confirmation can be
  implemented independently for LINA's client-only exit. LINA #452 additionally
  requires an idle-empty hint and automatic expiry; these are LINA requirements,
  not claims of identical upstream behaviour. Esc retains LINA's turn interrupt.
- **Unknown:** the issue author's reference revision, upstream terminal repeat
  handling, restoration and hint expiry. Upstream was read, not executed.

## Work panel stages (#770)

The stage view consumes Core's `stages` display data and keeps navigation refs
separate. `work_stages.go` owns entry selection, folding and pager context;
`work_steps.go` keeps a selected step and its live worker rows together while
scrolling. Snapshot replacement preserves the card/stage/step selection by its
refs and drops expansion when the entry closes. Old Core projections without
step refs remain readable without guessing an identity from a name.

`work_panel_stages_test.go` covers the stream projection, both widths and glyph
sets, closed-entry disclosure, replacement, 82-step scrolling and step Details.
The installed-state Core/PTY acceptance is `TestM3WorkPanelStages`, using the
fixed work-panel corpus and its E6/E7 evaluators with real stub worker processes.
