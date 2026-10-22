# Web search and page reading

This contract implements issue #440 alongside [runtime tools](runtime.md#tools)
and [credentials](integrations.md#credentials-and-sign-in).

## Reference observations before implementation

Reference: Hermes Agent, MIT, commit
`81a55437f3f31a9f12a30c53b6c5d49d420898aa`, `tools/web_tools.py`,
`tools/web_tools_extract.py`, `tools/web_tools_truncate.py` and
`plugins/web/firecrawl/provider.py` in the
[upstream repository](https://github.com/NousResearch/hermes-agent).

- **Seen:** search defaults to five results, clamps the limit to 1–100, and
  returns title, URL and description without fetching full pages. Extraction
  returns readable Markdown without a summarizing model. It rejects secret-bearing
  and private-network URLs, limits returned text (15,000 characters by default),
  and clearly identifies truncation. Missing configuration produces a setup
  notice. Firecrawl supports both operations with one credential; requests are
  bounded and interruptible.
- **Inferred:** these are appropriate boundaries for LINA's ordinary conversation
  tools. LINA should supply identity, permission and network approval through its
  existing registry, and treat every fetched field as untrusted evidence.
- **Unknown:** paid-provider availability, search freshness and relevance, and
  extraction quality on arbitrary live sites are not established by stub tests.
  LINA does not port Hermes' provider fallback ladder, local full-text cache,
  multi-URL extraction or image handling in this issue.

## Conversation tools

`web_search(query, limit)` returns title, URL and snippet. An omitted limit is
five; explicit integer limits are clamped to 1–100. `web_extract(url)` returns
readable page text with a clear truncation marker after 15,000 Unicode characters.
Empty queries, unknown arguments and non-public or credential-bearing URLs are
refused. Results are bounded and fenced as untrusted web content. Instructions
inside them cannot confer authority. Answers grounded in search results cite
their URLs.

Both tools use the existing tool permission and network approval path. An
approval precedes any provider request. Their running activity is visible in the
TUI and ends on success, failure or cancellation. With no provider configured,
neither tool is offered; the agent is told plainly that web access is not set up.
Credentials belong only to the existing secret store and the provider request;
they never enter conversation history, tool arguments, results or diagnostics.

## Provider setup

The first provider is Firecrawl, using the reference's `/v2/search` and
`/v2/scrape` endpoints at `https://api.firecrawl.dev`. Search maps description to
snippet; extraction requests main-content Markdown. There is no silent provider
fallback or configurable credential destination. The API client refuses redirects,
limits the response to 2 MiB and its request to 55 seconds, within the tool's
60-second deadline. Extraction validates public DNS answers before submission
and validates any returned final URL. Provider errors do not expose response
bodies or transport diagnostics. Arbitrary remote redirect chains and upstream
DNS behavior remain the provider's responsibility; Core never fetches page URLs
directly. Actual paid-provider quality and availability remain unverified by stubs.

The TUI's `/web` settings screen (also listed in `/help`) takes one Firecrawl API
key in a transient masked form. `s` sets or replaces it, `x` removes it, and `r`
refreshes configuration status. Esc and reconnect discard the form. It never
uses the composer, undo history or conversation ledger. Saving reports that a
key is configured, not that an upstream account was validated. The settings
operation itself makes no provider call.
While a settings request is pending, the screen shows that it is checking and
withholds the status and editing controls until Core's response arrives.

Core stores the key under the authenticated owner's `firecrawl/api_key` entry in
the existing secret store. Credentials are pinned for the duration of a turn;
replacement or removal during a turn is refused and may be retried afterwards.
Every new turn, including another agent's conversation and after Core restart,
reads the current configuration. Removing the key disables both tools on the
next turn. Configuration grants no network permission: existing once, always
and deny decisions still apply to search and page-origin reads.
