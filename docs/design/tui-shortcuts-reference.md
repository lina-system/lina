# Shortcut sheet and footer reference

This records observations before implementing issue #453. The implementation
contract is [T-10](tui.md#input-and-commands-t-10); upstream code is not copied.

- **Seen:** Hermes Agent revision
  [`944c3a434071f43e64b443d033f3eba30c64434c`](https://github.com/NousResearch/hermes-agent/blob/944c3a434071f43e64b443d033f3eba30c64434c/hermes_cli/cli_status_bar_mixin.py),
  `_status_bar_segments`, chooses ordered information at widths below 52, below
  76 and wider. Plain and styled renderers share those segments. The styled
  renderer checks display-cell width and can trim overflow. This was source
  inspection, not a runtime observation.
- **Inferred:** the progressive disclosure principle can apply to LINA's local
  shortcut hints. LINA reserves the connection identity and first hint, then
  admits a priority prefix of complete hints. Its no-partial-hint rule is a LINA
  requirement, not a claim that Hermes implements that same guarantee.
- **Unknown:** the revision and behaviour of the originally requested local
  Hermes snapshot were unavailable. Codex CLI's question-mark discovery and
  Gemini CLI's shared key table were specified by the issue; those implementations
  were not inspected for this change. No Hermes, Honcho, LCM-X or CRW runtime was
  executed.

Attribution is in [Terminal UI behaviour references](../../THIRD-PARTY-NOTICES.md#terminal-ui-behaviour-references).
