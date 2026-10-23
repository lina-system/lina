# Research: messengers in LINA

The person talks with other people in many messengers. LINA should read them, search them with evidence and, only where the person allows it, act in them. Two existing pieces do this outside LINA today: agent-messenger, a set of messenger command-line tools, and murmur, the owner's local message store. This note reads both, decides what LINA does with each platform, maps murmur's model onto Core and plans the move. It gives the evidence for [Messengers](../design/integrations.md#messengers) in integrations.md, [Messenger records](../design/conversation-and-memory.md#messenger-records) and [Sender identity](../design/conversation-and-memory.md#sender-identity) in conversation-and-memory.md, and [Messengers](../design/main-authority.md#messengers) in main-authority.md (#969). It is not normative.

No message data was read for this note: no account, token, message or murmur store. Only source code, docs and public terms pages. Terms quotes were checked against the live pages on 2026-10-23.

## Sources

| What | Revision | How it was read |
| --- | --- | --- |
| agent-messenger | [github.com/agent-messenger/agent-messenger](https://github.com/agent-messenger/agent-messenger), tag `2.39.1`, commit `9d2de96a43fe3e0e172e8e399454c475146a2697` (2026-10-20). MIT. TypeScript on Bun, published for Node.js 22.13 or later. | A read-only clone: source, tests and docs. Nothing was run, installed or signed in. |
| murmur | thisisjun786/murmur (private), commit `9cfacee2c1cdad9b6c10dff289549563ada33fdd` (2026-10-20). Python 3.12. | Source, tests and docs. Its live store was not opened. |

agent-messenger has 19 command-line tools over 12 platforms and about 58,000 lines of platform code without tests. Each platform is one directory under `src/platforms/`. A user mode acts as the person's own account; a bot mode acts as a bot or business account. The package also exports each platform as a TypeScript SDK.

## Platforms

"Official" means the platform's documented API used as documented. A row marked "no" automates the person's own account through a private or reverse-engineered client. The first four columns describe upstream at the revision above.

| Platform, mode | Auth upstream | Read | Write | Official | Terms risk | Decision |
| --- | --- | --- | --- | --- | --- | --- |
| KakaoTalk, personal | Email and password, then a passcode confirmed on the phone. Registers as an Android tablet sub-device. Token in a plaintext file (mode 0600). | Chats, history by `log_id`, members, live events (SDK only) | Send, reply, upload, mark read, leave | No: LOCO, reverse-engineered, imitates the official app | High: [Kakao abnormal-use policy](https://talksafety.kakao.com/policy/stability/abnormalusage), [Kakao terms §12](https://www.kakao.com/policy/terms?type=ts) | Add-on, read-only |
| Discord, bot | Bot token | Servers, channels, history with `before`/`after`, threads. Text needs the privileged message-content intent. | Send, edit, delete, react, threads | Yes | Low | Port (extend LINA's Discord adapter); writes by grant |
| Discord, personal | The person's session token: QR sign-in, or taken from the desktop app or a browser | Servers, direct messages, history, search, unread state | Everything the client can do | No: a self-bot | High: [Discord guidelines](https://discord.com/guidelines/) | Add-on, read-only, QR sign-in only |
| iMessage | None. macOS Full Disk Access to read, Automation to send. Runs on the signed-in Mac through the `imsg` tool. | Chats, history from `chat.db` with a ROWID cursor | Send text, tapback on the latest incoming message | No public API; local macOS features | Low to medium: [macOS licence](https://www.apple.com/legal/sla/docs/macOSTahoe.pdf) | Port to Go on a Mac Node; writes by grant |
| Slack, personal | A client session token and cookie, from QR sign-in, the desktop app or a browser | Channels and history with a cursor, threads, search | Send, edit, delete, react, upload, pins and more | Partly: documented methods with a session token, plus unpublished ones | Medium: [Slack developer policy](https://docs.slack.dev/developer-policy/) | Port with the person's own Slack app and an OAuth user token; writes by grant |
| Slack, bot | Bot token | Channels the bot is in | Send, edit, delete, react | Yes | Low | Covered by the Slack port |
| Microsoft Teams | Cookies from the app or a browser, or a device-code login under Microsoft's own Teams client id | Graph plus internal chat, search and real-time services | Send, edit, delete, react, upload | Partly: Graph is official; the internal services are not | High as built: [Microsoft APIs terms](https://learn.microsoft.com/en-us/legal/microsoft-apis/terms-of-use) | Port Graph only, with the person's own Entra app; work and school accounts only |
| Webex, personal | Browser token, a password login posing as the Webex web client, an OAuth device grant, or a personal access token | Spaces, messages (no history cursor), members, live events | Send, reply, edit, delete, upload | REST yes; the internal conversation API no | Low on REST, high on the internal API: [Webex developer terms](https://developer.webex.com/terms-of-service) | Port REST with the person's own Webex integration; writes by grant |
| Webex, bot | Bot token | Direct messages and group mentions only | Send, edit, delete, upload | Yes | Low | Covered by the Webex port |
| Telegram, personal | Phone code through TDLib, with the person's own `api_id`. Without one, upstream scripts my.telegram.org to create it. | Chats, newest N messages | Send, edit | Yes: the official client API on the person's account | Medium: [Telegram API terms](https://core.telegram.org/api/terms), [api_id](https://core.telegram.org/api/obtaining_api_id) | Port with gotd/td and the person's own `api_id`; writes by grant |
| Telegram, bot | Bot token | Updates only; the Bot API cannot read history | Send, edit, delete, react, upload | Yes | Low | No source: stays LINA's Telegram channel |
| WhatsApp, personal | Linked device by QR or pairing code (Baileys) | History pushed once at link time, then live messages; upstream keeps at most 500 per chat in memory | Send, edit, react | No: reverse-engineered | High: [WhatsApp terms](https://www.whatsapp.com/legal/terms-of-service) | Add-on, read-only |
| WhatsApp, Cloud API | Business phone number and token | Nothing without a webhook | Send, templates, reactions | Yes | Medium, and AI assistants as the main use are banned: [Meta WhatsApp Business terms §4.7](https://www.facebook.com/legal/Meta-Terms-for-WhatsApp-Business-Platform) | Drop |
| LINE, personal | QR and PIN as a secondary Android device; Letter Sealing keys stored without a file mode | Chats, newest N messages; no cursor survives a restart | Send text | No: reverse-engineered | High: [LINE terms](https://terms.line.me/line_terms?lang=en) | Add-on, read-only |
| Instagram, personal | Password and 2FA posing as the Android app, or browser cookies | Inbox, newest N messages, search by scanning | Send | No: private mobile API | High, even for reading: [Instagram terms](https://www.facebook.com/help/instagram/581066165581870) | Drop |
| Channel Talk, desk | Cookies from the desktop app or a browser | Team chats, user chats, search | Send | No: internal desk API | Medium: [Channel terms](https://channel.io/ko/terms) | Drop |
| Channel Talk, Open API | Access key and secret | User chats, groups, messages | Send as a bot, close chats | Yes | Low | Drop for now: a business support desk, not the person's messenger |
| WeChat, Official Account | App id and secret | Followers only; messages need a webhook | Customer-service and template messages | Yes | Low | Drop |

Terms in their own words:

- Kakao: "카카오가 허용하지 않은 비정상적인 프로그램 등을 이용하여 카카오톡의 프로토콜・API 등을 부정하게 호출하는 행위" (calling KakaoTalk's protocol or API through programs Kakao has not allowed) is listed as a reason Kakao "즉시 카카오톡 전체 서비스에 대한 이용을 영구적으로 제한할 수 있으며" (may at once restrict all KakaoTalk services permanently). Kakao's own message API only sends between users of one app; nothing official reads a personal account's chats ([Kakao developers](https://developers.kakao.com/docs/ko/kakaotalk-message/common)).
- Discord: "Do not use self-bots or user-bots." and "Each account must be associated with a human, not a bot."
- Telegram: "You must obtain your own api_id for your application." Also, "all accounts that log in using unofficial Telegram API clients are automatically put under observation", and "for flooding, spamming, faking subscriber and view counters of channels, you will be banned forever".
- WhatsApp: "you must not directly or through automated means: (a) reverse engineer, alter, modify" and no "bulk messaging, auto-messaging, auto-dialing".
- LINE: no "act of wrongfully operating the Services by using BOT, cheat tools or other technical means" (chapter 1, §15).
- Instagram: no "accessing or collecting information in an automated way without our express permission".
- Slack: no "Using unpublished APIs".
- Microsoft: "Do not try to circumvent them" (of access credentials), and no use "in any manner that works around any technical limitations".
- Webex: "You will not scrape or otherwise create copies of any data obtained using the Services" beyond what the app needs, with the users' consent (§8.17).

### Decision rules

1. **Read first.** A platform is in when LINA can read the person's chats with a stable message id.
2. **An official route is ported to Go** inside Core, with a maintained Go library or plain HTTPS. Its writes exist, off by default, per chat ([main-authority.md](../design/main-authority.md#messengers)).
3. **Where the terms forbid automating a personal account, the platform stays read-only and LINA says so** on the connect card, citing the term. Reading through such a client can break the terms too; the card says that as well, and the person accepts it before connecting.
4. **Among those, the add-on follows upstream instead of a port** when the protocol is reverse-engineered, has no maintained Go library and upstream fixes it often. KakaoTalk has no maintained Go LOCO library (node-kakao is marked not maintained; the Go attempts are a 2022 project and a research repository).
5. **Drop** a personal mode with no history cursor, a high ban risk and a login that imitates the official app (Instagram), and every business-only or webhook-only API: LINA runs no public listener ([integrations.md](../design/integrations.md#transport)), and those APIs do not reach the person's own chats.
6. **Never, in any mode:** taking tokens, cookies or passwords from other apps or browsers (upstream does this for Slack, Discord, Teams, Webex, Instagram, Channel Talk and KakaoTalk's macOS cache); signing in as an official client with its embedded client id or secret (Teams, Webex); scripting a developer portal (Telegram); evicting the person's other devices (KakaoTalk `--force` or the PC slot); reads that mark messages read or show presence.
7. **Licences.** whatsmeow and the Webex Go SDK are MPL-2.0, and mautrix/meta is AGPL-3.0. None of them can be linked into LINA ([dependencies.md](../policy/dependencies.md#adding-or-updating-a-dependency)). Webex therefore uses plain HTTPS, and WhatsApp uses the add-on.

### Upstream churn

| Platform | Commits since 2026-04-23 | All commits | What dominated |
| --- | ---: | ---: | --- |
| kakaotalk | 80 | 152 | Maturing: uploads, members, replies, exact 64-bit ids, lossless pages, safer sync state, avoiding room entry on reads. The version constants have not changed since 2026-04-02. |
| teams | 65 | 129 | Internal-API fixes: tenant and region lookup, fallback routes |
| webex | 44 | 101 | Login work and end-to-end key fixes |
| discord | 39 | 124 | Features, and fixes when the gateway stopped delivering events |
| discordbot | 32 | 52 | Features |
| slack | 30 | 153 | Features and sign-in |
| instagram | 29 | 57 | Login hardening, a real-time client |
| line | 22 | 49 | Letter Sealing decryption, long polling |
| webexbot | 20 | 20 | Added in this window |
| slackbot | 15 | 39 | Features |
| imessage, telegrambot | 9 each | 9 each | Added in this window |
| channeltalk | 8 | 30 | Cookie extraction broke on an app rename |
| telegram | 6 | 35 | Edit support, interop fixes |
| channeltalkbot, whatsapp, whatsappbot, wechatbot | 3 to 5 | 13 to 26 | Refactors |

Unofficial modes need steady upkeep, and that upkeep is what LINA would buy by following upstream rather than porting. Official modes barely churn upstream; their Go ports owe upstream nothing after the first read.

## The messenger add-on

- **What it is.** LINA's own small TypeScript program. It imports the pinned agent-messenger SDK for KakaoTalk, Discord personal, WhatsApp personal and LINE personal, and offers read operations only, as newline-delimited JSON on standard input and output. It stays running for each account, so the platform sees one session instead of a fresh login per command: upstream's KakaoTalk command does a full booking, check-in and login on every call.
- **Where it runs.** On the main, as a child of Core, like the Claude adapter ([runtime.md](../design/runtime.md#processes)). It needs Node.js 22.13 or later on the person's machine. Upstream's optional native packages (TDLib, koffi) are left out because Telegram is ported.
- **Sign-in.** The person signs in through the add-on's own flow: KakaoTalk's passcode, Discord's QR code, LINE's QR code and PIN, WhatsApp's pairing code. LINA relays what the person types and keeps none of it. The session lives in the add-on's own data directory, outside backups, and the person signs in again after a restore. This is the rule LINA already follows for a command-line tool's own login ([integrations.md](../design/integrations.md#credentials-and-sign-in)).
- **Read-only by construction.** The program's operation table holds only listing chats, reading a page of messages, members, who-am-I and live events. Write methods have no route. Reads that touch read state, such as KakaoTalk's read watermarks, which enter the room, are left out. Contract tests with recorded synthetic responses prove both.
- **Sandbox.** It runs in the OS sandbox with network on, writing only in its data directory.
- **Following upstream.** One change per adopted upstream release: review `git diff` of the used platform directories, `src/shared` and the vendored LINE library between the pinned and target tags; look for new write calls inside read methods, new hosts and new credential reads; update the pin and lockfile; run the contract tests; list behavior changes. The pin is an npm dependency under [dependencies.md](../policy/dependencies.md), so the change also passes the dependency check.
- **Following upstream for ports.** Each Go port records in its package documentation the upstream revision it was compared with. The same release review notes whether a change matters to the port (rate limits, id formats, new endpoints).

## murmur today

murmur gathers KakaoTalk, Discord and iMessage into one local store on the owner's always-on Linux machine. The issue counts about 850,000 messages; this note did not open the store.

| Part | What it does |
| --- | --- |
| KakaoTalk live | A systemd timer every 30 minutes runs a read-only wrapper over agent-messenger's KakaoTalk tool: list chats, then page new messages per chat into JSONL files. The upstream read window is about two days; a chat without a successful read for 40 hours is flagged as a gap risk. |
| KakaoTalk archive | A one-time import of an archive made from the Mac desktop app's data (`katok`), keyed `chat_id-log_id`, so it merges with live rows. Archive values win over live ones. |
| Discord | JSONL exports, one file per channel, read incrementally by byte offset and hash |
| iMessage | A read-only `.backup` copy of `chat.db`, fetched from a Mac over SSH and re-read only when its hash changes |
| Spool | `spool/events.jsonl`, append-only batches of unmasked messages and collection observations; the authority. The database is rebuilt from it. |
| Database | SQLite: `messages` keyed `(source, room_id, native_id)` with a UID that hashes the three; rooms, people, cursors, observations, integrity runs, FTS5, chunks and sqlite-vec vectors |
| Masking | Before database writes: known key prefixes, JWTs, bearer values, named assignments, sensitive URL query names and URL passwords, and an entropy rule outside URLs |
| Search | FTS5 with a Korean substring fallback, vectors over chunks of up to 10 messages split at gaps over 5 minutes, and hybrid by reciprocal rank. Remote `qwen/qwen3-embedding-8b` at 1536 dimensions, or a local 384-dimension model. |
| Integrity | Declared collection windows per room; statuses ok, filled, stale and unrecoverable |
| MCP | Read-only tools over stdio: `search`, `thread`, `person`, `recent`, `rooms`, `transcript`, `people`, `stats` |

## murmur mapped onto Core

| murmur | Core |
| --- | --- |
| Spool of unmasked batches | The ledger. Each message is a messenger record admitted in a canon transaction and masked at admission. LINA keeps no unmasked copy; the murmur spool stays with the person as their backup. |
| `messages` row | One messenger record: an event in the chat's messenger conversation ([Messenger records](../design/conversation-and-memory.md#messenger-records)) |
| Key `(source, room_id, native_id)` and its UID | The legacy reference `(platform, account, message, chat/native id)`, unique, so a second sync or import adds nothing. An imported row also keeps `(murmur, data home, uid, <uid>)`. |
| `author_id` equal to the person's own account, or `me` | Speaker kind `person`, when the platform route proves the connected account wrote it |
| Any other author | Speaker kind `external_participant`, with the platform id and display name. Memory about them only through a usable identity link ([Sender identity](#sender-identity)). |
| `edited_ts` | A new revision event that `supersedes` the earlier one |
| `deleted` | A deletion the platform reports hides the record through the one retraction path. A message missing from a later read is never a deletion. |
| `reply_to_uid`, `thread_root_uid` | Relations `replies_to` and `in_thread` |
| `attachments` | Metadata in the record. Bytes are fetched only when the person asks, as an import. |
| `raw` | Preserved body fields, masked |
| `ingest_path`, `filled_from` | Origin `messenger` with the ingest path; for KakaoTalk the archive value wins over live for one key |
| `rooms` | A chat in the messenger source registry: name, kind, first message time |
| `people` (one row per author id, names never merged) | Sender identities, linked to people records only by the identity rules |
| `cursors`, `sync_checkpoints` | A cursor per chat, committed with the records |
| Observations and integrity | A coverage record per read, a freshness state per chat, and a notice when a gap passes the platform's read window |
| FTS5 and the Korean fallback | The one index: trigram and Hangul pairs ([Store and recall](../design/conversation-and-memory.md#store-and-recall)) |
| Chunks and vectors | Message windows: derived navigation documents, one vector each, built by LINA's embedding adapter with the same 10-message, 5-minute rule. murmur's vectors are not imported. |
| Hybrid ranking | The one ranking: BM25 and vector distance fused by reciprocal rank |
| Masking rules | `kit/secret`, plus murmur's URL query-name and URL password rules. The entropy rule is not adopted: it masks ids and hashes, and LINA keeps no unmasked copy to undo it. |
| MCP tools | `recall` with origin `messenger` and filters for platform, chat, author and time (`search`, `person`, `recent`); `expand` around a result (`thread`, `transcript`); `messenger_chats` (`rooms`, `stats`) |
| Refresh timer | Each source's sync inside Core or the add-on's live events; text index in the same transaction, vectors in the background |

Store size was not measured. At the [unified-store](unified-store.md#index-size) ratio of 2.7 index bytes per text byte, every 10 MB of message text adds about 27 MB of index; short messages add more per byte through their document rows. The import rehearsal reports the real numbers before anything is written.

## Learning from messages

The owner decided on 2026-10-23 that the person's own messenger messages feed memory automatically, like the person's turns in the app. Other people's messages may become memory about those people, but only when the sender is matched to a known person with high confidence; unmatched and ambiguous senders stay search-only ([Sender identity](#sender-identity)). To keep the cost bounded:

- Only messages that arrive after a source is connected are learned. The source records a learning watermark when it connects.
- Lachesis reads them in batches on the extraction job's schedule and budget, groups a burst in one chat as one utterance, and makes no call when nothing new arrived.
- History older than the watermark (a chat's first fill and imported history, such as murmur's) is searchable. It is learned only when the person starts an import with learning on, either of murmur's store or of a connected source's older history; the card states how many of the person's messages it covers and an estimated model cost.
- The person can turn learning off for a source or a chat. A chat in the `never-leaves` class is never learned from.
- The extraction request carries the person's messages and, as untrusted context, at most a few messages before each. Only the person's own messages can be evidence of an item about the person. A matched sender's messages can be evidence only of items about that sender, and only after the identity gate passes.

The number of the person's own messages in the murmur store is unknown; the rehearsal counts it per source, and how many other senders a rehearsal could match.

## Sender identity

The design is in [Sender identity](../design/conversation-and-memory.md#sender-identity). The choices behind it:

- **Platform ids are the only automatic key.** A Discord, Telegram or KakaoTalk user id is stable on its platform; Slack and Teams ids are stable inside a workspace or tenant. murmur already keeps one `people` row per `(source, account, author_id)` and never merges by name; LINA's people records follow the same rule ([materials-and-knowledge.md](../design/materials-and-knowledge.md#knowledge-documents-and-people-records)).
- **Only shared identifiers link across platforms on their own.** A phone number or email address that two identities show, or a contact card that lists them, is the only evidence strong enough without the person. iMessage and WhatsApp key senders by phone or email, Slack and Teams profiles show an email, and the Mac Node and the Google connection can read the person's contacts. As upstream reads them, KakaoTalk, Discord and LINE show no phone or email for other people, so their senders link across platforms only through the person.
- **Names never decide.** Two people share a name; one person writes under different names on different platforms and changes nicknames. A name match only asks.
- **Ambiguity is detected, not resolved by guessing.** Shared family phones, team mailboxes and recycled numbers are the known ways an exact identifier misleads. Each makes the link unusable until the person answers.
- **Repair rides the retraction path.** Because each learned item cites the link it relied on, removing a link reaches every item it produced in one transaction, as a forgotten event does today.

### Identity evaluation

The matcher is measured before any learning about other people, and again on every change to it.

| Set | What it holds | Where it lives |
| --- | --- | --- |
| Synthetic | Generated people with identities on several platforms and the hard cases: the same name for different people; one person under different names; nickname changes over time; a family phone shared by two people; a recycled number; a team mailbox; a contact card listing two people's numbers; Slack ids in two workspaces; the person's own second account | `testdata`, in the repository |
| Owner sample | At least 300 sender identities from the owner's sources, labelled by the owner with the people record each belongs to, or "unknown" | On the owner's machine only. The repository gets counts and rates, no names, ids or text. |

Measures: precision of usable links (`exact` without ambiguity, and `confirmed` from accepted proposals), the share of identities linked automatically, how many ambiguous cases are flagged, top-1 precision of proposals, and repair: after a link is removed, no item cites it, and only that identity's messages are queued again.

Gate: no wrong usable link on the synthetic set, and no wrong usable link among the owner's labelled identities. With 300 or more labelled identities and none wrong, the wrong-link rate is below 1 percent at 95 percent confidence (the rule of three). Until both hold, learning about other people stays off.

## Migration

Following the data-pipeline method: the sink key is the legacy reference, every write is an upsert on it, every replay is bounded, and a gate runs before readers see a load.

| Step | What happens | Gate | Rollback |
| --- | --- | --- | --- |
| 1. Rehearsal | The murmur importer reads a copy of the stopped murmur data home (spool and config), read-only, after a schema gate on the spool event version and message fields. The person confirms their own account id per source. It reports counts per source and room, the person's own messages, masked spans, refused rows and Discord rooms with path-derived ids. It writes nothing to canon. | Every row is mapped or refused with a reason | None needed |
| 2. Import | Records go through messenger admission, keyed by the legacy reference. Learning runs only if the person turned it on. The import ends with a backup generation. | Per room: the count and the SHA-256 of the sorted murmur UIDs (murmur's `manifest` digest) match. A second run adds nothing. | Delete the import batch; keys make a new import clean |
| 3. Parallel run | LINA's KakaoTalk source runs beside murmur's timer for at least 7 days | Over the overlap, no message id that murmur has is missing in LINA | Stop LINA's sync |
| 4. Cutover | The person stops murmur's timer and MCP. LINA is the collector. | LINA's search answers the person's usual queries | Restart murmur's timer; its spool is untouched |
| 5. Retire | After 30 days the person may delete murmur's data home. LINA never deletes it. | | |

The iMessage source replaces murmur's SSH copy once the Mac Node runs it. Discord history imported from exports stays a source in the import-only state until the person connects Discord.

## Security review

Method: security-safety-review. Verdict: **HOLD**. This is a design; no code, scan, add-on or sign-in was run.

Plan: the targets are the messenger sources, the add-on and the store. Platform content and the add-on's output are untrusted. The add-on is semi-trusted code that holds a messenger session and the network. Allowed: reads. Writes: only on official platforms, by grant.

| Part | Reads | Writes | Calls | Holds |
| --- | --- | --- | --- | --- |
| Ported adapter | Platform APIs | Canon through Core; platform only by grant and approval | Platform HTTPS | Tokens in LINA's secret store |
| Messenger add-on | Platform through upstream | Its own data directory | Platform hosts | Its own sign-in session |
| iMessage on a Mac Node | `chat.db` under a grant | Nothing on the Mac; writes later by grant | Messages automation | No token |
| Store | Records | Canon | Embedding endpoint, if the person allows it | Masked text |
| Extraction | The person's new messages | Memory items | The memory model | |

Findings and remedies:

1. Upstream keeps sessions in plaintext files (0600), and the vendored LINE library writes its key file with no mode. The add-on's data directory is 0700 and outside backups; ported adapters use LINA's secret store.
2. Upstream takes tokens, cookies and a cached password from other apps and browsers, which can raise keychain prompts. The add-on offers no route to those commands.
3. The same SDK can write. The add-on has no write route, a contract test proves it, and the sandbox limits the rest.
4. Some reads have side effects (room entry, read watermarks). They are left out, and adapter tests with fake platforms assert that no state-changing request is sent.
5. Message text is untrusted and can carry prompt injection. It enters requests as external evidence with its origin and never as an instruction. Other people's text becomes memory only about its own sender, through a usable identity link, after the identity gate passes.
6. A wrong identity match would file one person's words under another. Links need a shared identifier or the person's answer; ambiguous links are unusable; removing a link retracts what it produced; the evaluation gates learning.
7. Other people's words reach outside services only as bounded extraction context and, when the person turns it on for a source, through the embedding endpoint. Secret spans never leave.
8. Unofficial reads can get the account limited. The card cites the terms; the person accepts; the KakaoTalk add-on uses the tablet slot without forcing it.
9. Supply chain: agent-messenger brings 22 runtime packages (Baileys, Thrift, Blessed and others). They are pinned by lockfile, run with no lifecycle scripts and are reviewed with every release.

| Action | Policy |
| --- | --- |
| List chats, read pages, members, live events | Allowed |
| Embedding a source's records | Asks once per source; the person's choice |
| Learning from history older than the watermark | Asks in an import; the person's choice |
| Learning about another person | Only through a usable identity link, after the identity gate passes |
| Linking a sender by name or other weak signals | Asks the person |
| Send, reply, edit, upload, delete, mark read on an official platform | Needs the chat's write grant and asks every time |
| React on an official platform | Needs the chat's write grant; reported with undo |
| Any write on a read-only platform | Blocked |
| Token, cookie or password extraction; forced device takeover; developer-portal scripting | Blocked |
| The murmur data home and its spool | Observed only |

Proposed deny list for the owner's agent environment while murmur's wrapper still runs, not applied: `*agent-kakaotalk auth extract*`, `*agent-kakaotalk*--force*`, `*agent-kakaotalk message send*`.

Missing evidence: the add-on contract tests, a dependency check over the add-on lockfile, a sandbox run, a real sign-in on a test account, and the identity evaluation.

## Service contract

Method: backend.

| Caller | Trust | Calls |
| --- | --- | --- |
| A tool call from the model | Untrusted arguments, trusted conversation binding | `recall` over messenger records, `messenger_chats`, write tools |
| The person in the app or TUI | Owner | Connect, sign in, grants, approvals, import |
| The add-on | Semi-trusted, sandboxed | JSON lines, validated and size-bounded |
| A platform API | Untrusted content | Validated responses |
| A murmur data home | Untrusted data, read-only | Spool and config |

| Failure | Result | Retry |
| --- | --- | --- |
| Sign-in expired or revoked | The source shows needs sign-in; other sources continue | After the person signs in |
| Rate limited | The cursor is kept; the source waits | After the platform's retry-after |
| Account refused or banned | The source fails with that cause | Never automatic |
| Unknown response shape from the add-on or platform | The batch is refused; the cursor does not move; the source fails with "upstream changed" | After an add-on or adapter update |
| A gap longer than the platform's read window | A coverage gap is recorded and the person is told | None; a later archive import can fill it |
| Write outcome unknown | Never sent again; settled by reading the chat when the platform allows it | No |
| The add-on exits | Restarted with backoff; the cursor is intact | Yes, bounded |
| An import schema mismatch | Refused, naming the difference | After an importer update |

A messenger tool result has one shape: the source and chat, then items with record id and revision, time, author (platform id, display name, whether it is the person), a masked excerpt and the legacy reference, then the chat's coverage (fresh until, known gaps) and a next-page cursor. Errors use Core's existing tool error envelope with the failure class above.

## Implementation issues

Filed in M3. Each names its design sections, its areas and what it depends on. Learning about other people (#1077) also waits for the identity gate on the owner's sample.

| Order | Issue | Change | Depends on |
| --- | --- | --- | --- |
| 1 | #1069 | Messenger store: sources, chats, records, cursors, coverage, edits, source deletions, URL masking rules | #869 |
| 2 | #1070 | The messenger add-on: pinned upstream, read-only program, sign-in relay, sandbox, sync review | #1069 |
| 3 | #1071 | KakaoTalk through the add-on, read-only | #1070 |
| 4 | #1072 | murmur import: rehearsal, import, reconciled counts | #1069 |
| 5 | #1073 | Parallel run, cutover and retiring murmur's collector | #1071, #1072 |
| 6 | #1074 | Messenger search: recall filters, context, chats, message windows with vectors | #1069, #871, #874 |
| 7 | #1075 | Learning from the person's own new messages | #1069, #872 |
| 7a | #1076 | Sender identity: identities, links, matcher, merge and repair, and the identity evaluation | #1069 |
| 7b | #1077 | Learning about matched senders, behind the identity gate | #1075, #1076 |
| 8 | #1078 | iMessage on a Mac Node | #1069, #205, #1012 |
| 9 | #1079 | Discord bot source | #1069 |
| 10 | #1080 | Slack with the person's own app | #1069, #1028 |
| 11 | #1081 | Telegram personal with gotd/td | #1069 |
| 12 | #1082 | Teams through Graph | #1069 |
| 13 | #1083 | Webex through REST | #1069 |
| 14 | #1084 | Write grants and messenger writes | #1069, #1080 |
| 15 | #1085 | Discord personal through the add-on, read-only | #1070 |
| 16 | #1086 | WhatsApp personal through the add-on, read-only | #1070 |
| 17 | #1087 | LINE personal through the add-on, read-only | #1070 |
