# Migrating a Wiki Schema or Language

Use when an existing markdown knowledge base needs new folders, filenames,
language or frontmatter. Rewrite pages to the target schema; keep the archive intact.

## Procedure

1. Inventory every source page: path, title, tags, aliases, sources, confidence,
   word count and first body line. Normalize block lists and inline list strings
   before grouping pages.
2. Show migrate and exclude lists. Leave retired operational material and content
   outside the target domain in the archive; ask the owner about borderline pages.
3. Plan each target folder, unique filename and page type using the current
   SCHEMA.md. Reject invalid filename characters and verify that every inventory
   entry has a disposition. Inspect the plan for stale tags that misroute pages.
4. Build an old-path and old-title to new-title link mapping. Rewrite links using
   it; turn links to excluded pages into plain text.
5. Rewrite one page at a time with the full target schema. Preserve tables, lists,
   numbers, quotations, citations and evidence levels. Translate aliases as needed,
   preserve original technical names, and update source paths without losing provenance.
6. Pilot two different page types. Validate both and read one output fully before
   processing the rest. Use bounded parallelism and a manifest of validated outputs
   for resumability; file size alone does not prove completion.
7. Compare each output with its source: frontmatter parses, required sections exist,
   tables and lists retain their content, and every distinct figure and citation
   is accounted for. Review language-dependent size differences rather than applying
   a universal character-count ratio. Rework only flagged pages.
8. Copy only referenced raw sources, preserving relative paths. Check links,
   rebuild index.md and record the migration in log.md before publishing the staged
   pages. Report counts by folder and every exclusion.

## Pitfalls

- Never overwrite the archive or silently omit source content.
- Do not carry old system jargon or agent-specific forms of address into pages.
- Use the configured model provider for batch work, not a machine-specific endpoint.

## Verification

Every inventoried page is migrated or explicitly excluded. Target filenames are
unique, links resolve, cited raw files exist, and the index matches the published pages.
