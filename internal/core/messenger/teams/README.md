# Teams through Microsoft Graph

Core reads a work or school account through the person's Entra application.
It uses Go's HTTP client, Graph v1.0 and the tenant's Entra v2 token endpoint.
There is no SDK, internal Teams API, cookie import or built-in Microsoft client.

## Setup

1. In your organization's Entra admin center, create an app registration for
   accounts in your organizational directory. Copy its application (client) ID
   and directory (tenant) ID. Personal Microsoft accounts are unsupported.
2. Add the **Mobile and desktop applications** redirect
   `http://localhost/auth/callback`. Entra ignores the port for localhost native
   redirects; Core uses the client listener's port, or 8400 for pasted callbacks.
   Use a public native client; do not create or supply a client secret.
3. Add these **delegated** Microsoft Graph permissions: `User.Read`,
   `User.ReadBasic.All`, `Chat.Read`, `ChatMember.Read`, `Team.ReadBasic.All`,
   `Channel.ReadBasic.All`, `ChannelMessage.Read.All`, `ChannelMember.Read.All`.
   Sign-in also requests `offline_access`. Obtain administrator consent where
   your tenant requires it. A tenant refusal is shown as unsupported.
4. Send a configuration request to the running Core, then sign in:

   ```sh
   lina teams --socket <client-socket> --request teams-config.json
   lina teams --socket <client-socket> --request teams-sign-in.json
   ```

   `teams-config.json` contains public settings:

   ```json
   {"operation":"configure","id":"work","tenant":"<directory-id>","client_id":"<application-id>"}
   ```

   `teams-sign-in.json` contains `{"operation":"sign_in","id":"work"}`.
   For a remote terminal, add `--paste`; open the printed URL in your browser
   and paste the complete redirected URL. The browser may report that the
   loopback page cannot load. The callback belongs to the original client
   connection and expires after ten minutes.

The command accepts a JSON request on stdin when `--request` is omitted.
`list` returns connection status and a short failure reason. `chats` takes `id`
and returns that connection's chats. `switches` takes `id`, `chat`, `sync`,
`learn` and `embed`. `disconnect` takes `id`, cancels sync and deletes tokens;
previously read records remain. Changing a registration disconnects its previous
source. Signing into a different Graph user requires a new connection ID.

## Storage and polling

`teams_connection` holds only public registration, account and expiry metadata.
Access and refresh tokens are an opaque, atomically rotated secret-store entry
under the owner. No credential appears in protocol results or canon.

Each connected account has its own cancelable worker, adapter and token binding.
Workers resume on Core start, poll every minute, honor longer Retry-After values,
and stop for a sign-in or tenant-policy refusal. One throttled source cannot
stall another. Chat switches are read before each sync; an in-flight page can
finish before a switch takes effect. Disconnect waits for the canceled worker.

Chats page `/me/chats`; channels page `/me/joinedTeams` and each team's channels.
Chat messages use matching descending `lastModifiedDateTime` order and `gt`
filters, overlapping the high watermark by one second. Channel message and reply
endpoints do not support those filters: Core pages them and applies the watermark
locally. Every root's replies are paged, even when the root itself is old. Each
page is sorted by creation time before admission. The cursor retains the high
watermark until all pages commit; only a complete read adds coverage.

Explicit `deletedDateTime` values become tombstones through messenger admission.
Missing messages never imply deletion. Sender IDs are `tenant:user`; app authors
use `tenant:app:id`. Profile `mail` is evidence stored in `messenger_author`, not
an identity key. A deleted directory user keeps its platform identity without an
email. Foreign-tenant users are not looked up in the connected tenant's directory.
HTML bodies become plain text. Attachments are metadata only; download, writes,
subscriptions and sovereign-cloud endpoints are outside this source increment.

Pagination may only continue the same Graph collection. All redirects are
refused. Request bodies, diagnostics and callback secrets never enter logs.
A missing `iss` is accepted for Entra's single, fixed tenant authority; a supplied
issuer must exactly match it. The normal client listener relays its numeric
loopback address, so callback validation accepts localhost and 127.0.0.1 at the
same expected port/path. Neither is an inbound Core endpoint.

## Reference and evidence

Seen: the repository's [messenger research](../../../../docs/research/messengers.md)
excludes upstream's cookie/internal-service routes. Microsoft's documented
[chat paging](https://learn.microsoft.com/en-us/graph/api/chat-list-messages?view=graph-rest-1.0),
[channel paging](https://learn.microsoft.com/en-us/graph/api/channel-list-messages?view=graph-rest-1.0),
[reply paging](https://learn.microsoft.com/en-us/graph/api/chatmessage-list-replies?view=graph-rest-1.0)
and [native redirects](https://learn.microsoft.com/en-us/entra/identity-platform/reply-url)
set this implementation's request shapes.

Inferred: channel changes require a full paged traversal with local filtering;
email must never replace tenant-scoped identity. Unknown: a real organization's
consent policy and retained history. No live tenant verification is claimed.

The synthetic HTTP tests cover paging, edits, soft deletion, replies, profiles,
PKCE/state/issuer binding, refresh, throttling and rejected destinations.
`TestM3TeamsGraphInstalledCore` runs a child Core with the real owner socket
boundary, signs in through the relay, imports Graph data, restarts, observes an
edit/deletion, disconnects and reports missing consent. Only Microsoft HTTP is
replaced by synthetic responses.
