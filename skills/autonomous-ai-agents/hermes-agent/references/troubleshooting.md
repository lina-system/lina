# Troubleshooting

1. Run `lina doctor` and record the actual error before changing settings.
2. Resolve the active configuration with `lina config path`. Check profile and cwd before diagnosing missing state.
3. Inspect `lina logs` or `lina logs gateway` without copying secrets into the report.

## Model failures

Inspect OCX reachability, selected model identifier, endpoint, and admission credential. Check auxiliary/delegation routing separately. Do not switch to a vendor provider or add unrelated keys as a generic repair. Verify with a bounded `lina chat --oneshot -q <prompt>`.

## Missing tools or skills

Use `lina tools list` and `lina skills list`. Check platform enablement through `lina skills config` and service credentials. Begin a fresh session after changing prompt-visible capabilities.

## Messaging and audio

Use `lina gateway status` and the gateway log. For Discord check Message Content Intent; for Slack check subscribed channel events. Verify STT/TTS configuration and actual audio independently of OCX inference. Do not restart a busy process without accounting for its active work.

## Changes not visible

Read the setting back and identify whether it is startup-only. Restart the affected process or start a new conversation as appropriate. For stale extracted pages inspect web-cache settings and exemptions; do not assume a cached result proves the origin is unchanged.

## Desk or OS access

Confirm the Desk with `lina desk show --json`. Explicit working directories and resumed-session directories can differ from the default. For macOS access inspect LINA Helper permissions and installation under `helper/`; a running CLI does not establish screen or accessibility permission.
