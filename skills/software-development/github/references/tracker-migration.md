# Move open work into GitHub Issues

1. Inventory open issues across every source team and project. Match each project
   to a repository using current code and tracker references, not name alone.
2. Agree which work migrates and which is cancelled. Preserve the source as a
   read-only record; do not delete it. Never create a repository by guessing.
3. Reuse existing organization labels and milestones. Use Summary / Done when /
   Context bodies, preserving acceptance criteria, dependencies and source links
   only where their visibility permits. Generalize private details before posting.
4. Persist source key to GitHub number and REST ID after each creation so a
   restart resumes without duplicates. Resolve cross-references in a second pass;
   attach sub-issues using REST IDs, not issue numbers, in a third pass.
5. Link the destination from the source record before closing it. Use full URLs
   across trackers; bare issue numbers may resolve to the wrong repository.
6. Verify per-project issue and milestone counts, then sweep all source teams for
   remaining open work. Count equality alone cannot detect an omitted project.
7. Update active instructions and integrations to point at GitHub within the
   authorized scope. Keep historical records marked as historical.

Use bounded batches and respect API rate-limit responses. Review the migration
report against live destination reads before claiming completion.
