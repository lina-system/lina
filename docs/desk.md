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

The installer runs `hermes desk init --adopt`, which creates `~/Desk` or keeps the one already there.
By hand:

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

## Checking it

`scripts/desk_acceptance.py` installs fresh in a temporary home, confirms the layout, has two agents
write and read the same Desk file through the file tools, and checks the path in the API.
