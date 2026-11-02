---
name: onepassword-agents
description: "Use credentials without exposing secret values."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos, windows]
metadata:
  hermes:
    tags: [1password, credentials, secrets]
    category: productivity
    related_skills: []
---

# OnePassword Agents Skill

Use the 1Password CLI to give a consuming process only the credentials it needs.
Never print secret values, including prefixes or recovery codes.

## When to Use
Use for authorized API authentication or credential provisioning, not for exploring unrelated accounts.

## Prerequisites
Install `op` using the official 1Password instructions. Configure desktop integration or
a scoped service account through an approved secret channel; never request its token in chat.
Service-account authentication uses `OP_SERVICE_ACCOUNT_TOKEN` supplied by the runtime.
Use `terminal` with tracing and command-output capture of secret values disabled.

## How to Run
The following Bash example uses identifiers supplied by the owner, not real vault or item names.
The consumer must read its credential from the environment and must not log it.

```bash
op whoami >/dev/null
API_TOKEN='op://<vault-id>/<item-id>/<field-id>' op run -- <consumer-command>
```

On other shells, assign the reference using that shell's environment syntax before `op run`.

## Quick Reference
- Authentication check: `op whoami` with output suppressed.
- Discovery: `op item list --vault <vault-id>`; inspect only authorized metadata.
- Consumption: secret references resolved by `op run` into the child environment.
- Reference syntax: `op://<vault-id>/<item-id>/<field-id>`; prefer stable identifiers.

## Procedure
1. Confirm the consuming service, authorized vault, field, and required permission scope.
   Check authentication without displaying account details; report failure without the token.
2. Locate the existing item through authorized metadata. Do not retrieve a whole item as JSON
   merely to inspect its field labels: that response can contain secret values.
3. Pass a reference to `op run` and run the consumer once. Resolve within that process;
   do not expand the secret into command-line arguments or shell-generated commands.
4. Validate with a non-destructive service request. Report only success, failure, or missing
   permissions, never the response body if it could contain credentials.
5. If the credential is absent, check for duplicates and ask the owner to provision it in
   1Password. Create an empty item only when requested; use existing organizational tags.
6. For an authorized destination that accepts secrets on standard input, pipe directly to
   that consumer without logging. For unavoidable files, use a private temporary directory,
   restrict file access to the owner, and remove the files immediately after consumption.

## Pitfalls
- Do not enable shell tracing, disable masking, dump environments, or print secret fragments.
- Do not put values in briefs, logs, issues, commits, screenshots, or persistent plaintext files.
- Do not execute configuration text to load credentials or read unrelated personal vaults.
- Do not rotate, delete, distribute, purchase, or change access without explicit authorization.
- Treat a credential pasted into chat as exposed; request rotation through the approved channel.

## Verification
Confirm authentication and the consumer's result without revealing values. Check temporary
files are removed and only the intended process received the credential; masking alone is not proof.
