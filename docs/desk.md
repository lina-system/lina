# The Desk

The Desk is one folder of the person's files that every agent starts from and shares. It is laid out
as PARA:

| Folder | What goes there |
| --- | --- |
| `Inbox` | Anything new that has no place yet. Agents sort it. |
| `Projects` | Work with an end date, one folder per project. |
| `Areas` | Ongoing responsibilities with no end date. |
| `Resources` | Reference material and topics of interest. |
| `Archive` | Finished projects and anything no longer active. |

## Creating it

The installer runs `hermes desk init --adopt`, which creates `~/Desk`, keeps the Desk already
configured (including one made with `--path`), or keeps the folder already there. By hand:

```sh
hermes desk init                       # create ~/Desk
hermes desk init --path ~/Documents/Desk
hermes desk init --path ~/Work --adopt # use an existing folder, keeping every file
hermes desk show                       # path and folders (--json for scripts)
```

Adopting never deletes or overwrites anything: it adds the missing PARA folders and an `AGENTS.md`
only when the folder has none. Numbered folders such as `1_Projects` or `4_Archive` count as their
PARA folder, so an existing layout is not doubled.

## One setting for all agents

The path lives in `desk.path` in the default profile's `config.yaml`. Every profile reads it from
there, so changing it (`hermes desk init --path ...`) moves every agent. A profile's own `desk.path`
is ignored.

An agent whose `terminal.cwd` is unset (or `.`, `auto`) works in the Desk: the gateway, the
multiplexed gateway's per-profile scope, and the app's default folder (`GET /api/fs/default-cwd`)
all resolve to it. An explicit `terminal.cwd` still wins. The interactive CLI keeps working in the
folder it was started from.

## Naming a file on the Desk

Records, the app and workers name a Desk file as `desk:<path>`, for example
`desk:Projects/launch/plan.md`. A path that leaves the Desk (through `..` or a symlink) is refused.

- `GET /api/desk`: `{path, exists, folders, agents_md, config_key, ref_prefix}`.
- `GET /api/desk/resolve?ref=desk:Projects/launch`: `{ref, path, exists, is_dir}`, or 400.
- A kanban task can work in a Desk folder: `hermes kanban create ... --workspace dir:desk:Projects/launch`.

## Rules agents keep

The Desk's `AGENTS.md` is loaded into every agent's context when it works in the Desk. It says
where things go and the rules: agents sort the Inbox, finished projects move to Archive, nothing is
deleted without the person's approval, and one agent edits one file at a time. The last rule is
also enforced: a write to a Desk file takes a lock that agents in other processes wait on.

## Sharing a Desk report

Write the report as Markdown under `1_Goals` or `2_Areas` on the configured Desk. Register the file
and send one short summary with the URL printed by the command.

```sh
lina desk register '2_Areas/example/weekly-update.md'
```

Registering again refreshes the same page. Only share reports intended for the recipient. Keep
personal material on the Desk. Registration refuses personal folders, links and paths that match the
report deny list. Set up `report-deny` in the default LINA home with one Desk-relative glob per line.
The command also reads `$XDG_CONFIG_HOME/sion-board/report-deny` (under `~/.config` if unset).
At least one of those policy files must exist.

### Report address decision

Use the LINA dashboard behind the owner's sign-in as the address for new Desk reports. The operator sets
`dashboard.public_url` to the dashboard's reachable HTTPS base URL in the default profile.
`HERMES_DASHBOARD_PUBLIC_URL` takes precedence when set to a valid URL. Registration appends
`/reports/<id>/page` to that base. With no valid base configured, it prints only the path.
Configure the base before sending links from chat.

Keep the host and `/r/<slug>` path of links already sent in chat. LINA serves each imported slug
directly through the same authenticated report page as `/reports/<id>/page`. Redirecting at the old
dashboard would break those links when that service stops. Transferring its host and port to LINA is
a separate operational change.

Before cutover, apply the store migrations, configure the Desk and its deny list, and run
`lina desk import-reports <registry-directory>` against the copy containing `index.json`, Markdown
and `.files` directories. A failed entry rolls back the batch. A retry validates the snapshot and
preserves later Desk edits. Run `python scripts/check_report_links.py <registry-directory> <base-url>`
against the signed-in dashboard using the session option described by `--help`. Check all active
titles and all withdrawn addresses before enabling sync with the original Desk mounted.

### Independent sync schedule

`lina desk sync-reports` runs one pass over registered reports. It refreshes edits and follows
recognized moves within the allowed folders. Missing and newly denied reports stop being served.
A read failure keeps the last copy, reports the failure on stderr and gives the pass a nonzero exit
status after processing the other reports.

The Linux systemd [service](systemd/lina-desk-reports.service) and [timer](systemd/lina-desk-reports.timer)
examples run every five minutes, with a catch-up run after downtime. They run independently of the
gateway, brain schedule and `lina cron ops`. From the repository root, install them as the account
whose default LINA profile has the configured Desk, report policy and store access. The service
looks for `lina` in `~/.local/bin` and the standard system directories. If the launcher is installed
elsewhere, set `ExecStart` in the copied service to its absolute path followed by `desk sync-reports`.

```sh
unit_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
install -d "$unit_dir"
install -m 0644 docs/systemd/lina-desk-reports.service docs/systemd/lina-desk-reports.timer "$unit_dir/"
systemctl --user daemon-reload
systemctl --user enable --now lina-desk-reports.timer
systemctl --user list-timers lina-desk-reports.timer
```

Use `lina desk sync-reports` for an immediate pass and
`journalctl --user-unit=lina-desk-reports.service` to inspect scheduled runs.

### Cutover and rollback

Once a newly registered link opens through the dashboard sign-in from the recipient's surface, switch
the deployed shared `report-pages/SKILL.md` and any profile `SOUL.md` reporting instructions to the
registration command above. Check the configured Desk's `AGENTS.md` for any reporting instructions to
update. These deployed files are outside the repository.

To roll back, run `systemctl --user disable --now lina-desk-reports.timer` and
`systemctl --user stop lina-desk-reports.service`. Restore the deployed guidance and publish through
the legacy path with `report-link "<Desk path>"`.

## Checking it

`scripts/desk_acceptance.py` installs fresh in a temporary home, confirms the layout, has two agents
write and read the same Desk file through the file tools, and checks the path in the API.
