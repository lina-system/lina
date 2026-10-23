# Claude Code worker reference

Read before implementation for #134. Authority: C-29 and
[completion signals](../../../../docs/design/work-and-delegation.md#completion-signals).

- **Seen:** installed Claude Code 2.1.304 `--help` and `--version` expose
  `--print`, `--output-format stream-json`, `--verbose`, stdin prompts and an
  optional model. No permission, provider, plugin or settings override is needed.
- **Seen:** Anthropic's `claude-agent-sdk-python` commit
  `163f50ed56fa8c3fd9346148b6e56e7696794b58`,
  `src/claude_agent_sdk/_internal/message_parser.py` and
  `transport/subprocess_cli.py`, describe `system` initialization, assistant/user
  messages and `result` with `subtype`, `is_error`, `session_id`, `result`,
  `usage`, `permission_denials` and `errors`. Streaming output uses verbose mode.
- **Inferred:** a matching final result ends the turn before exit; missing,
  malformed or foreign-session results fail. Permission denials and error
  results cannot imply delivery. Core derives source-labeled evidence from the
  result and fixed reads of the assigned branch's PR, then retains its existing
  manager acceptance boundary. Unknown informational events never finish a turn.
- **Unknown:** no authenticated Claude tool-execution round trip is claimed.
  Synthetic fixtures exercise the documented protocol and the running Core.
  Interactive control replies, resume and active steering are not advertised.
  The supplied Hermes and CRW checkouts are absent on this machine; no behavior
  or code from those projects is ported and no review workflow is executed.
