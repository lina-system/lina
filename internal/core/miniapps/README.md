# Mini apps

Core's registry and origins for the small web apps LINA builds on request. The
contract is in [`docs/design/surfaces.md`](../../../docs/design/surfaces.md#mini-apps)
and the client payloads are in
[`docs/design/host-protocol.md`](../../../docs/design/host-protocol.md#mini-apps).

## As built

- The registry is migration 64 (`mini_app`, `mini_app_revision`,
  `mini_app_change`, `mini_app_event`). Revision files, captures and recipes live
  under `<state root>/miniapps/<id>`; the store lives beside them as `store.json`.
- An app origin binds on the person's first open. With a tailnet address
  configured, the same port is bound there too, and the frame page derives its
  app origin from the host it was reached on.
- `boot` supplies the closed vocabulary: reads `todos` and `running`, and the
  request `todo.done` (which runs `write_record` as the owner agent under the
  ordinary grant check). Any other name is refused.
- Only the authenticated `mini_app_request` client operation records the
  person's allow, deny, keep, undo, restore and remove. The `mini_app` model tool
  can list, create, change and read history, and cannot record a decision.
- A declared schedule is validated, shown with the app, and not run yet. A
  background run holds read-only tools, so a scheduled store write needs its own
  path through the authority gate before it can be built on.
- Pre-live checks need an installed Chromium. Without one, a revision is refused
  with `mini_app_browser_unavailable`; a model score is never substituted.

## Tests

`go test ./internal/core/miniapps/` covers the registry, the gateway, the policy
and the browser checks; the browser tests skip when no Chromium is installed
(set `LINA_CHROMIUM` to point at one). `go test ./e2e/m4 -run
TestMiniAppsThroughRunningCore` builds an app through a running Core.
