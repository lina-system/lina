# Inventory legacy skill layers

Use `python -B -m hermes_cli.migrate_skills` for a read-only inventory of the skill packages and session databases in the homes you select. Give each home a distinct label. Use `--shared` for a skill directory visible to every selected home, `--home-tree` for an additional directory visible to one home, and `--bundle` for an installation's skill directory. Repeat an option for additional trees. The command reads `<home>/skills` and `<home>/state.db` for each label.

```sh
python -B -m hermes_cli.migrate_skills \
  --home h0=/snapshot/first --home h1=/snapshot/second \
  --shared /snapshot/shared-skills --home-tree h1=/snapshot/extra-skills \
  --bundle /snapshot/install/skills
```

The default output is JSON on stdout. Add `--manifest /reports/skills.json` to create a new file outside the input trees. An existing output path or an unreadable database stops the inventory. `--exclude FILE` accepts one exact skill name per line. Every listed name appears in `exclusions`, including names absent from the inputs. The installed `lina migrate skills` command accepts the same arguments after the general CLI performs its usual active-home startup.

## Manifest fields

The envelope carries `version`, `homes`, `window_end` (Unix seconds), `exclusions`, `counts`, and `records`. `counts.names` and `counts.locations` are the filesystem census before exclusions. `counts.excluded_names` counts matched names, and `counts.records` counts the emitted name-and-hash pairs.

| Record field | Meaning |
| --- | --- |
| `name` | Frontmatter name, with the package directory name as the fallback. |
| `layer` | `common` when every selected home has the name. All other names have layer `agent`. |
| `owners` | Sorted home labels with access to this particular copy. A bundle-only copy has an empty list. |
| `sha256` | Package fingerprint over sorted relative file paths and their byte digests, including supporting files. |
| `copies_identical` | True when all locations for the name have the same fingerprint. |
| `locations` | Physical package directories holding this fingerprint. Each has `path`, source `kinds` (`home`, `shared`, `bundle`), and `owners`. Multiple views of one directory count as one place. |
| `calls_30d` | Total assistant `skill_view` calls with this name across selected session databases in the inclusive 30-day window. |
| `file_calls_30d` | Subset of those calls containing the `file_path` argument. |
| `same_name_overlap` | True when two or more physical package directories declare the name. |
| `weak_overlap` | Other names whose descriptions share the first three whitespace-delimited words, compared case-insensitively. |
| `archive_candidate` | True when `calls_30d` is zero and `same_name_overlap` is true. A weak overlap alone leaves this false. |

Discovery uses the catalog's `SKILL.md` convention, including linked package directories and the catalog's exclusions for runtime directories and nested support material. All discovered versions are inventoried, including copies normally shadowed by lookup precedence. Bundle tracking files describe the installed origin. The inventory hashes the package currently on disk so that local edits survive.

For the role-based index in lina#567, select records whose `owners` include the requesting home. The union of eligible records for a common name covers every home. Differing common copies retain their individual locations and owners, including any shared fallback. A bundle-only record has no eligible home. Archive flags are advisory and leave indexing eligibility unchanged.

The call counters belong to the **name** and repeat across its variants. Count them once per name when totaling calls. SQLite aggregates within the window and returns only names, path-presence groups and counts. Malformed tool calls contribute zero. A missing database stops the command so that missing evidence cannot turn into an archive flag.

If `state.db` uses write-ahead logging and has active `-wal` and `-shm` files, supply a filesystem-enforced read-only view with both sidecars present. SQLite can otherwise create a sidecar or update a read mark even when the database connection uses `mode=ro`. A checkpointed database with closed writers can be inventoried in place. An unstable or incomplete snapshot fails the run. Keep the sidecars with their database when preparing a snapshot, since they may hold the latest committed calls.
