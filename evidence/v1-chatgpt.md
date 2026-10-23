# Owner-run ChatGPT V1 acceptance

[V1 is pending the owner's sign-in](v1-chatgpt.json). Recorded-wire tests verify
the implementation and the probe; they do not establish live eligibility,
redirect acceptance, or request-field acceptance. PR #306 is part of #81 and
must not close it until the required live observations exist.

Run from the repository root on the checkout being accepted. The command below
builds an opt-in acceptance executable, then hosts the production account store
and client service with private, separate state. It connects through the real
client socket and performs LINA's own dynamic registration. No existing worker
login or credential file is read.

```sh
v1_dir=$(mktemp -d)
go test -c -o "$v1_dir/chatgpt-v1" ./internal/core/accounts
"$v1_dir/chatgpt-v1" -test.v -test.timeout=25m \
  -test.run='^TestV1OwnerSignIn$' -v1-owner-signin \
  -v1-state-root="$v1_dir/state" \
  -v1-report="$v1_dir/local-observation.json" -v1-mode=local
```

Open the printed authorization URL in the system browser and approve LINA. The
loopback listener starts before the URL is printed. The command receives the
redirect, validates the code/state/issuer and ID token in Core, stores the
credentials privately, requests one serialized refresh, and reads the rotated
credential only after persistence. It selects the first visible model from the
account's model catalog; pass `-v1-model=<listed slug>` to select one explicitly.

It then runs three completed Responses turns with `store=false`, `stream=true`,
encrypted reasoning retained in the next request, and two synthetic parallel
function calls including the one-string `apply_patch` function. These tools
return fixed results and never execute a proposed patch. Separate probes record
acceptance or rejection of `parallel_tool_calls`, `service_tier` and
`prompt_cache_key`; the three turns test `include: ["reasoning.encrypted_content"]`.
The cache-identity probe sends a fixed probe-only value in the shape a
conversation's identity takes, never a conversation's identity, and records
acceptance only for that shape. Probes do not expand the production manifest's
request-field list.

Exercise the no-listener path separately:

```sh
"$v1_dir/chatgpt-v1" -test.v -test.timeout=25m \
  -test.run='^TestV1OwnerSignIn$' -v1-owner-signin \
  -v1-state-root="$v1_dir/state" \
  -v1-report="$v1_dir/pasted-observation.json" -v1-mode=pasted
```

After authorizing LINA, paste the complete redirected URL into the command.
The browser may show an unavailable loopback page in this mode; copy the URL
without changing the host, port, path or query. The URL contains a short-lived
code: keep the terminal private and do not copy it into an issue or report.

Each observation file is created at mode 0600, refuses overwrite, and contains
only the mode, time, model, completed-turn count, sign-in checks and field/tool
observations. It contains no account identifiers, tokens, authorization codes,
opaque reasoning content or model output. A failed step leaves partial evidence
and returns a failing exit status. A rejected redirect, missing reasoning,
missing tool call or incomplete response must be investigated before acceptance.
The issuer may require waiting until the permitted refresh time before the
rotation check succeeds; no retry or account fallback is performed.

Both modes above host Core and the client on the same machine. Running the
pasted mode remotely exercises a browser on another machine, but it does not
by itself prove a separate remote client connection. The V1 requirement for a
remote client must be observed through an authenticated remote client transport
using the same relay. Keep `remote_client` unobserved until that run is recorded.

After the required local and remote runs, populate the pending record only from
the sanitized observations: registration, redirects, scopes, ID-token checks,
refresh rotation, at least three completed turns, reasoning resend, function
and `apply_patch` calls, parallel calls, accepted request fields and tool
placement, and each undocumented field's acceptance or rejection. Include the
checkout revision and observation time. Do not treat this command's
`partial_live_observation` status as full V1 acceptance. Keep the pending status
while any required observation is absent.

The private state directory contains real OAuth credentials. Keep it outside
Git, protect it like a provider login, and disconnect LINA in ChatGPT settings
when the test registration is no longer needed. Remove the temporary executable
and observation files after recording the sanitized evidence and deciding how
to retain the private account state.
