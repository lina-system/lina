# Worker recovery reference observations

Read before implementation for #135. C-29, C-58 and the stall/failure contract
remain the design authority.

- **Seen:** CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
  `internal/bridge/appserver/bus.go`, reconciles exact turn IDs by reading all
  pages of turns and, when necessary, items. Missing or unreadable items are
  distinct from an empty list. Reconciliation sends no new turn. The transport
  reports an interrupted write as uncertain and refuses to resend it.
- **Inferred:** Core must retain the original assignment and exact turn before
  reconciling, fence replacement bindings, retain unknown effects on failed
  reads, and commit the one recovery allowance before sending continuation.
  A terminal turn alone is not evidence that an external effect succeeded.
- **Unknown:** an arbitrary worker command's external effects cannot be settled
  from its exit code or prose. Unsupported or incomplete observations remain
  unknown. Claude's headless stream supplies no read-only persisted-thread API.
  Hermes/plugin sources were unavailable; no Hermes, Honcho or LCM-X behavior
  is ported. No reference commands, workers or review workflows were executed.

The headless adapters retain the terminal stream snapshot before retry admission.
A final failure with a complete observation containing no tool activity can be
settled. Tool activity, unknown stream events, missing results and lost requests
retain uncertainty. A stored snapshot is not invented for an unobserved process.
