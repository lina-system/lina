# Manual mail and Slack collection

`lina sources` reads opted-in accounts into the existing v1 external-message spool. The operator initiates every provider read. The existing external-message ingest job can consume complete local spool lines independently. Mail and Slack are disabled on a fresh installation. The root switch `memory.external_messages.enabled` takes precedence over both opt-ins.

## Setup

Put the settings in the **host root** `config.yaml`. Select an existing Gmail connection by its CLI alias. An empty account list makes zero calls. Slack uses the authenticated hosted connector. Keep credentials in the provider's existing authentication mechanism. The example uses a synthetic alias and an absolute private spool path.

```yaml
memory:
  external_messages:
    enabled: true
    spool: /path/to/private/spool/events.jsonl
    sources:
      mail:
        enabled: false
        accounts: [sample-inbox]
        query: "newer_than:2d in:inbox -in:spam -in:trash -category:promotions"
        min_interval_minutes: 60
        max_calls_per_run: 10
        max_calls_per_hour: 20
        max_calls_per_day: 100
      slack:
        enabled: false
        min_interval_minutes: 60
        max_calls_per_run: 15
        max_calls_per_hour: 30
        max_calls_per_day: 150
```

Use `0700` for the spool directory and `0600` for its JSONL file. Each selected source needs `enabled: true` before a probe or collection. The Gmail query applies to every selected alias. Slack lists public and private conversations, DMs and group DMs accessible through the connection, then reads joined conversations and direct messages. Archived conversations are skipped.

## Manual commands

```sh
lina sources status
lina sources probe mail
lina sources probe slack
lina sources collect mail --dry-run --max-calls 3
lina sources collect slack --since 2026-01-01T00:00:00Z
lina sources collect mail --spool /path/to/private/spool/events.jsonl
lina sources resume mail
lina memory external-messages ingest
lina messages search "meeting notes"
lina messages open 'msg:messages:<collector-uid>'
```

`probe` makes one allowed list request if the source is enabled, due and within budget. `--dry-run` uses the same read path and counts candidate messages. Both spend real quota in an enabled installation and update safety state, including the minimum interval and any authentication stop. Neither appends events nor advances coverage. `--state-dir PATH` selects private collector state for any `sources` subcommand. `status` reports counts, time and fixed outcome categories and omits message bodies, addresses, room names and account identifiers.

| Collector | Reads per run | Rolling hour | Rolling day | Minimum interval | Messages per run |
| --- | ---: | ---: | ---: | ---: | ---: |
| Mail | 10 | 20 | 100 | 60 minutes | 100 |
| Slack | 15 | 30 | 150 | 60 minutes | 100 |

Configuration and `--max-calls` can lower these limits. The interval can be increased. Every page, message detail, thread and permalink request consumes one call before dispatch. A run stops on the first failed read. Authentication failures and inactive connections latch a stop immediately. HTTP 429 ends the attempt without retry. Three consecutive failures latch a stop until `resume`. Resume preserves prior usage and the minimum interval.

## Read scope and stored fields

| Source | Allowed tools |
| --- | --- |
| Gmail via Composio CLI | `GMAIL_FETCH_EMAILS`, `GMAIL_FETCH_MESSAGE_BY_MESSAGE_ID` |
| Slack via hosted connector | `SLACK_LIST_ALL_CHANNELS`, `SLACK_FETCH_CONVERSATION_HISTORY`, `SLACK_FETCH_MESSAGE_THREAD_FROM_A_CONVERSATION`, `SLACK_FIND_USERS`, `SLACK_RETRIEVE_MESSAGE_PERMALINK_URL` |

The collector has no send, edit, delete, reaction, membership, label, attachment download or calendar operation. Gmail first lists IDs, then reads matching messages. Text includes the subject and plain body, with HTML reduced to text when necessary. Slack follows threads of returned parents and uses the message permalink. The time window covers at most the previous two days for a new pass. `--since` can narrow that window. Text is capped at 4,000 characters per message. Attachments contribute filenames only.

The stable message identity is derived from platform, room and native ID. Gmail scopes thread rooms to the account alias. Slack uses the channel ID and the full timestamp, including fractional seconds. The stored citation carries the stable `msg:messages:…` reference, native ID and a redacted `display_url` or `permalink`. Both platforms use speaker `outside`, grade `external` and external quotation. Material returned to the model by recall stays inside `<external-data>`.

## Coverage, visibility and retention

The collector keeps separate per-source call history and per-mailbox or per-channel progress under the host root's `state/sources/`. Only completed windows advance coverage. Contiguous windows extend the covered range. A later window separated by a gap starts a new range. A capped run retains its unfinished position; replay after an interrupted append is safe because ingest deduplicates the stable message and version. Room history has the bounds of the listing and time window. A thread whose parent is outside that history may be absent.

The spool reader maintains its own committed byte offset and prefix hash in PostgreSQL. Readers independently apply current source visibility to stored rows and derived references. Disabling a source hides previously stored references during search, direct open, recall, sender/person lookup and timeline reads. Re-enabling it restores access. Existing vectors containing a hidden contributor are withheld as a whole. Mail and Slack timelines stay out of cached session cards even while the source is visible. Collection leaves the system prompt prefix and notes block unchanged.

The spool contains original external text **before** ingest redaction. Place it outside shared directories and apply the host's private backup and retention policy. The store redacts content and allowed metadata on ingest. Turning a source off retains spool bytes, database rows, coverage and budgets. Upstream deletion or absence from a later listing does not erase earlier local observations. Do not truncate or edit a consumed spool prefix. Handle local erasure and backup retention separately from the read switch. [Store and versioning details](store.md#external-messages)

Source contracts and static provider schemas are available in [the common contract](https://github.com/lina-system/lina/issues/546), [mail scope](https://github.com/lina-system/lina/issues/556), [Slack scope](https://github.com/lina-system/lina/issues/818), [Gmail toolkit](https://docs.composio.dev/toolkits/gmail.md) and [Slack toolkit](https://docs.composio.dev/toolkits/slack.md).
