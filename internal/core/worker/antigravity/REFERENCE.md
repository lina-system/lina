# Antigravity worker reference

Read before implementation for #211. Authority: C-29 and
[completion signals](../../../../docs/design/work-and-delegation.md#completion-signals).

- **Seen:** [official headless CLI documentation](https://antigravity.google/docs/cli/headless)
  (2026-10-22) names `agy`, `--output-format stream-json`, stdin prompts,
  `init` (conversation id and cwd), `step_update`, and terminal `result` with
  `status`, `response`, `usage` and `denied_actions`. A streaming input session
  does not exit at each result. Headless approval requests are soft denials;
  `control_request`/`control_response` are unsupported. No approval bypass is needed.
- **Seen:** CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
  `internal/review/agy/doc.go`, `envelope.go`, `classify.go` and the recorded
  `testdata/denied_actions.json`: agy 1.2.16 can return `status: SUCCESS`,
  `denied_actions: [{action: command, display_name: RunCommand}]`, and exit 0.
  Its stdin invocation avoids the observed `--print=-` prompt corruption.
- **Inferred:** result ends a worker turn immediately, independently of child
  exit. Missing/malformed results fail closed. Denials must remain visible.
  A success envelope is a worker claim; Core reads the task branch's PR and
  checks its head/checks/review before deriving a delivery receipt. No prose
  or exit code establishes success. Use LINA's existing process supervisor.
- **Unknown:** no local agy installation/account is available. Documentation
  examples and sanitized recorded envelopes exercise the wire boundary, not a
  live authenticated round trip. Current-version approval interactivity and
  active-turn steering are unverified and are not advertised. Hermes checkouts
  are absent; no Hermes behavior is ported. No CRW code or review loop is run.

`testdata/denied.jsonl` preserves the recorded denial fields with synthetic
identities and the documented stream wrapper. Other fixtures are synthetic
replays of the documented event shapes, with no personal prompts or output.
