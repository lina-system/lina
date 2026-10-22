# Input history behaviour reference

Reference: Hermes Agent, MIT, revision `e06df9c2`,
[`hermes_cli/cli_tui_mixin.py`](https://github.com/NousResearch/hermes-agent/blob/e06df9c2/hermes_cli/cli_tui_mixin.py).
Read before implementation; no upstream code is copied.

- **Seen:** `_tui_history_up` and `_tui_history_down` call the buffer's
  `auto_up` / `auto_down`. The binding comment specifies history at the
  first/last line and cursor movement inside multiline input. `_normal_input`
  excludes secret, connection, approval and other modal fields. Recall
  suppresses paste collapsing. The textarea uses `FileHistory`.
- **Inferred:** delegating to the buffer keeps the current draft while browsing;
  LINA must explicitly preserve its own unsent draft when returning past the
  newest entry. LINA's Bubbles textarea also needs a soft-wrap boundary check.
- **Unknown:** the reference was read, not run; its installed prompt-toolkit
  version, exact cursor restoration and history behaviour during reconnect or
  deletion were not verified.

LINA follows T-01: no file history. It derives newest-first recall entries from
the current conversation's accepted person blocks, excluding pending local
echoes, other conversations and non-person output. It keeps only the navigation
identity and unsent draft in memory. Authoritative replacement or focus changes
cannot reintroduce removed entries. Channel tokens and sign-in callbacks remain
in their existing isolated masked forms.
