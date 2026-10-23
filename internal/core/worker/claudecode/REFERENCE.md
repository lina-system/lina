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

## Implementation notes

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-29 headless worker implementation

`internal/core/worker/claudecode` implements C-29 with the installed `claude` headless process (`--print --output-format stream-json --verbose`).
The matching `result` ends the turn before process exit; missing results, invalid messages, reported errors and permission denials fail.
Core owns and reaps the child tree.
`dispatch_work` accepts `worker: "claude"` through the same durable admission, work allocation, PR checks and manager boundary as Antigravity.
An explicit selection keeps that adapter; omitted selection uses the quota and situation chooser in C-57, with Antigravity first among otherwise equal candidates.
The artifact retains the final result alongside the independently read PR; receipt provenance is `adapter-derived`.
No worker settings or integrations are installed.
The headless `e2e/m2/claude-worker.json` scenario exercises installed Core, actual Git work and durable receipts with synthetic worker and GitHub boundaries.
