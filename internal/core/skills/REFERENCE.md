# internal/core/skills implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-23 maintenance migration order

Migration 36 adds operation membership and a durable notice outbox, preserving the published work-waiting schema at version 28, records search at version 29, merge authority at version 30, worker dispatch at version 31, Discord at version 32, Korean records search at version 33, waiting decisions at version 34, preset effects at version 35 and all earlier migrations.

### C-23 skill store implementation

The #71 store publishes a metadata-only catalog in canon and keeps instruction files in immutable Git trees in `versions/skills/`, one folder per skill.
The composing owner installs `skills.Schema` in a numbered canon migration.
A write merges the submitted files with the current bundle, checks both the existing and resulting bundle, and requires the expected revision.
Automatic writes accept regular, non-executable UTF-8 Markdown with only one-line `name` and `description` front-matter scalars.
Unknown front-matter fields and executable/support-code files return `skill_requires_explicit_acceptance`; text inside a Markdown code example remains instruction text.
The automatic path adds no connection or permission.
