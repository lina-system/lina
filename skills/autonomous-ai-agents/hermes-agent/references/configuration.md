# Configuration and Toolsets

Use `lina config path` and `lina config env-path` to locate the active profile's files. Read a value with `lina config get <key>` and update it with `lina config set <key> <value>`. Never hand-edit configuration for the owner when the safe writer can make the change.

| Section | Inspect for |
| --- | --- |
| `model` | magpie provider, model identifier, endpoint |
| `agent` | Turn limits, reasoning, stop verification |
| `terminal` | Working directory, backend, timeout |
| `display` | CLI skin, language, reasoning display |
| `delegation` | Child model, capacity, iteration and depth limits |
| `auxiliary` | Per-task model routing |
| `security` / `approvals` | Redaction and command consent |
| `stt` / `tts` | Voice input/output providers |
| `memory` / `curator` | Memory and agent-created skill maintenance |

Check key names against `hermes_cli/config_defaults.py` and their runtime readers. Defaults vary; do not copy numeric defaults from another version.

Use `lina tools list` before claiming a tool is available. `lina tools enable <name>` and `lina tools disable <name>` change configured toolsets; start a fresh session for the new tool schema. A service-gated tool also needs its own credential. Check `toolsets.py` and `tools/registry.py` rather than maintaining a second catalog here.

Settings belong in `config.yaml`; secrets belong in `.env`. Do not use an environment variable to store a new non-secret behavior setting. For voice, verify the enabled STT/TTS provider and dependencies, then test real audio. Model inference access through magpie does not establish a voice service credential.
