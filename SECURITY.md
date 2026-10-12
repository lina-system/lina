# Security

## Report a vulnerability

Report a vulnerability or sensitive evidence to the repository owner directly, through a private channel. Never put vulnerability details, credentials, or personal data in an issue, a commit message, or a branch name.

A useful report includes the affected commit or version, impact, and a minimal reproduction. Use synthetic data and redact credentials, personal conversations, memory/persona data, user files, and operational logs.

## Supported versions

LINA has no released versions yet. There is no response-time commitment.

## Repository hygiene

Review files and evidence before committing or pushing them. Do not commit secrets, personal data, local installation details, or operational state; `.gitignore` is not a substitute for that review. A change to permissions, authentication, or data access explains its impact in its merge commit message.

Verification runs without credentials ([foundation.md](docs/policy/foundation.md#boundaries)).
