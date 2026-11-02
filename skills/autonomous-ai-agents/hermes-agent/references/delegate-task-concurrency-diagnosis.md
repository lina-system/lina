# Delegation Capacity Diagnosis

Do not attribute a smaller batch to the runtime without evidence.

1. Read `lina config get delegation.max_concurrent_children` for the owning profile.
2. Inspect the actual submitted `delegate_task` calls and returned errors. Was the requested tasks array sent at all?
3. Use `search_files` on the active profile's logs for `Too many tasks` or `Truncated` plus `delegate_task`. Inspect `tools/delegate_tool*.py` and the turn-loop capacity logic for the installed version.
4. Distinguish a per-call rejection, per-turn truncation, and a cost warning. A cost warning alone is not a cap.
5. If config, submitted count, and runtime results differ, report the observed boundary. Missing log lines alone do not prove the model self-limited: the wrong profile, log level, or code version can hide evidence.

Capacity is per parent, not necessarily a global process limit. Approval inheritance is not a concurrency throttle. Do not lower an intentionally chosen limit merely to hide the discrepancy. Reproduce with a bounded batch of independent read-only tasks and count completed results.
