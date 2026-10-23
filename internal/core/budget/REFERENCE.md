# internal/core/budget implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-10 budget increment

#53 implements `budget.Reserve` and `Settle` inside a caller-owned fenced canon transaction.
The caller installs `budget.Schema` in its migration.
Reservations count every agent and all seven run kinds against one token allowance; settlement is idempotent for identical usage and rejects a rewrite.
Each row retains agent, account, provider, run kind and run, with unreported usage encoded as null.
Known input plus output replaces the reservation charge without double-counting cached or reasoning subsets; if either total is missing, the charge retains at least the reservation until usage is known.
This charge is admission accounting, not reported usage.
No Core protocol or TUI operation exposes the budget yet.
