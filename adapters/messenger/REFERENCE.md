# Discord personal: behavioral reference

Read before implementation: the pinned `agent-messenger` 2.39.1 package
(commit `9d2de96a43fe3e0e172e8e399454c475146a2697`), files
`src/platforms/discord/client.ts`, `types.ts`, `remote-auth.ts`,
`credential-manager.ts`, `ensure-auth.ts` and `commands/auth.ts`. The package is
vendored into the dependency check only and is never run against a real account
here.

- **Seen:** a personal account is reached with a user token over Discord's v10
  REST API. Guilds come from `GET /users/@me/guilds`, a guild's channels from
  `GET /guilds/{id}/channels`, direct messages from
  `GET /users/@me/channels`, a guild's members from `GET /guilds/{id}/members`,
  and a chat's messages from `GET /channels/{id}/messages`, whose only paging
  parameters are `limit` (at most 100) and `before`, `after` or `around` a
  message snowflake. Channel types 0, 2, 5 and 13 carry a message timeline;
  categories, threads, directories, forums and media posts do not. A message
  reports an `edited_timestamp` when it changed; the API supplies no deletion
  feed to a personal account. `loginWithRemoteAuth` opens the remote-auth
  gateway, shows a QR payload to scan with the Discord mobile app, and returns
  the user token. `DiscordClient.login()` without credentials calls
  `ensureDiscordAuth`, which reads a token from the desktop app or a supported
  Chromium browser; `token-extractor.ts` does the same directly. The client also
  exposes sends, edits, deletes, reactions, read-state acknowledgement, unread
  and read-state reads, and typing.
- **Inferred for LINA:** take the QR flow only, and hand the SDK a token only
  from the add-on's own data directory, so neither the desktop-app extraction
  nor a browser is a path to a session. Read guild text channels and direct and
  group messages, page forward by snowflake around the newest message Core has
  so an edit is seen again as its later edited time, and never infer a deletion
  from absence. Skip a channel a bot-mode source of the same platform already
  reads, so the chat is read once and its records keep one identity by message
  id. Do not reach the SDK's write, reaction, read-state or typing entry points;
  `src/allowlist.ts` names the readable ones and the contract test proves the
  rest are absent.
- **Unknown here:** the platform's live behaviour, the real history depth and
  rate limits of a personal account, and whether an account that scans a QR code
  keeps its session across a reset of the add-on's data directory (the design
  says it does not). No personal account, token or credential is used by the
  synthetic acceptance tests; they establish LINA's protocol, admission and
  read-once behaviour only.
