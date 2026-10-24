# internal/core/messenger/webex implementation notes

Design authority: [integrations.md](../../../../docs/design/integrations.md#platforms),
[Credentials and sign-in](../../../../docs/design/integrations.md#credentials-and-sign-in),
[Consent through the client](../../../../docs/design/integrations.md#consent-through-the-client)
and [Messenger adapter boundary](../../../../docs/design/integrations.md#messenger-adapter-boundary).
Research: [messengers.md](../../../../docs/research/messengers.md) (Platforms: Webex).

## Seen, inferred, unknown

Read on 2026-10-24; nothing was run against a real account.

**Seen.**

- Webex publishes an OpenAPI description of its messaging API
  ([webex/webex-openapi-specs](https://github.com/webex/webex-openapi-specs/blob/main/public-spec/webex-messaging.json), `public-spec/webex-messaging.json`). The
  endpoints this adapter uses are `/rooms`, `/messages`, `/memberships` and
  `/people/me`, plus `/v1/authorize` and `/v1/access_token` for sign-in.
- `/messages` takes `roomId`, `max` (up to 100), `before` (ISO 8601 time) and
  `beforeMessage` (a message id). It answers newest first, in `items`, with a
  `Link` header for the next page. A message carries `id`, `roomId`, `text`,
  `markdown`, `personId`, `personEmail`, `created`, `updated` (only after an
  edit), `parentId` (a reply) and `files` (attachment URLs).
- `/rooms` takes `max`, `teamId` and `type`, answers `items` with
  `id`, `title`, `type` (`direct` or `group`) and a `Link` header.
- `/memberships` takes `roomId` and `max`; `/people/me` answers the connected
  person's `id`, `displayName` and `emails`.
- Rate limiting is HTTP 429 with `Retry-After` in seconds; the API docs state
  the same for 423. The API follows RFC 5988 link headers.
- The [integration docs](https://developer.webex.com/docs/integrations) and
  [Login with Webex](https://developer.webex.com/docs/login-with-webex) describe an OAuth 2.0 authorization-code flow against
  `https://webexapis.com/v1/authorize` and `https://webexapis.com/v1/access_token`,
  with `code_challenge`/`code_challenge_method=S256` supported and recommended,
  a registered redirect URI that must match the request, and a token response
  carrying `access_token`, `expires_in` (about 14 days), `refresh_token` and
  `refresh_token_expires_in` (about 90 days, renewed on use). Refresh uses
  `grant_type=refresh_token`, `client_id`, `client_secret` and `refresh_token`;
  a refused refresh answers `invalid_grant`.
- The research note records that LINA must not take browser tokens or cookies,
  must not sign in as the web client with its own client id, and must not use
  the internal conversation service; the Webex Go SDK is MPL-2.0, so the port
  uses plain HTTPS.

**Inferred.**

- `beforeMessage` provides an identity-based history cursor; a time cursor would repeat or skip messages that share a timestamp. This
  port therefore walks back with `beforeMessage` and keeps the newest message
  id as its tail anchor.
- An edit is not a separate event on this API: a re-read of a message that now
  has `updated` later than the stored revision is the edit
  (`Capabilities.Edits`). Webex reports no deletions, so an absent message is
  never a deletion (`Capabilities.Deletions` is false).
- This implementation uses polling rather than Webex webhooks, which need a
  reachable inbound endpoint (`LiveEvents` is false).
- Webex requires the redirect URI to match the registered one, including the
  loopback port. The client reports the port it listens on, and the person
  registers `http://127.0.0.1:<port>/auth/callback` in their integration.

**Unknown.**

- Live registration and consent in an actual Webex tenant were not tested.
  Public docs require an exact redirect match and show loopback examples, but
  do not establish acceptance by every integration or organization. The client
  uses a registered fixed port, and pasted callbacks use the same address.
- Whether an org policy can withhold scopes the person approved, and what
  status the API returns then. Any non-401/429 refusal is reported as the
  source's failure cause, which is the designed fallback.
- Whether `updated` can move to a value equal to the stored one on a second
  edit within the same instant. The store compares edit times, so such an edit
  would be skipped.
- The exact rate-limit window per endpoint. The adapter honours `Retry-After`
  and gives up a read when the wait exceeds a minute, leaving the cursor as it
  was.
- Whether message `files` URLs ever point off the API origin. The adapter
  refuses any attachment URL outside its base origin.

## One account, no writes

The source is the person's own Webex account, so the adapter sends only GET
requests to the API plus the two sign-in POSTs. There is no send, edit, delete
or mark-read path here; writes belong to #1084.

## Credentials

`Configure` records the person's own integration client id and secret under the
person's id (`webex/client_id`, `webex/client_secret`). `Refresh` and the code
exchange present the secret in the form body, read through the same temporary
header boundary Core's model account refresh uses. The token record
(`webex/tokens`) now carries the access token's own expiry
(`secrets.Tokens.Expires`), so the adapter refreshes five minutes before expiry
instead of waiting for a rejection. A refresh the platform refuses deletes the
record and reports `messenger.ErrNeedsSignIn`, which the sync loop turns into
the source's `needs_sign_in` state; a refresh that fails for a transport reason
is a source failure and is retried on the next read.

## Cursors

    webex/1/fill/<newest>/<before>
    webex/1/fill/<newest>/<before>/<stop>
    webex/1/tail/<newest>

A fill walks back through a room's first read. When the room has no older
messages left it hands over to the tail, anchored on the newest message the
fill saw. A tail read that overflows one page catches up backwards until it
reaches the message the last read stopped at, so a burst of more than one page
skips nothing. Message ids are base64 themselves, so cursor fields are
base64url-encoded.

## Rate limits

`Retry-After` is honoured by waiting rather than failing the source, because the
cursor is safe to keep and the person should not see a source failure for the
platform's own pacing. A wait longer than a minute, or a limit that outlasts the
read's attempts, ends that read with the source failing and the rate limit as
its cause; the cursor is untouched, and the next read that succeeds clears the
cause.


## Running Core and setup

The `webex` capability is installed in Core's normal boot. `lina webex --socket
<socket>` reads one JSON settings request from stdin and prints its result.
The client owns a fixed loopback listener on port 8400; it never silently chooses
another port when that port is busy.

1. Create your own integration in the Webex developer portal. Register
   `http://127.0.0.1:8400/auth/callback` and enable `spark:rooms_read`,
   `spark:messages_read`, `spark:memberships_read`, and `spark:people_read`.
2. Send `{"operation":"configure","platform":"webex","client_id":"...",
   "client_secret":"..."}` through stdin. Do not put the secret in shell
   arguments, conversation input or the repository. Core keeps it in its secret
   store; any temporary request file must be private and removed after use.
3. Send `{"operation":"sign_in","platform":"webex"}`. The installed client
   opens the system browser. For a remote terminal, save this non-secret request
   in a file and use `--request <file> --paste`; open the printed URL and paste
   the final callback URL. A failed loopback navigation is expected in paste
   mode; copy the address. The registered callback must still match exactly.
4. `{"operation":"list"}` reports state. To disconnect, send
   `{"operation":"disconnect","account":"<platform account ID>"}`.

One connected account is supported initially. Reauthentication cannot change
its platform identity. Disconnect before configuring a different integration
or account. Failed reads retry every five minutes; revoked credentials require
sign-in. No actual account, model provider, browser session, or private message
was used for verification. Synthetic headless acceptance exercises the client,
Core boot, relay, refresh, paging, canon, restart, rate limit and disconnect.
