# Terminal UI behaviour reference

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
