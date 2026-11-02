# LINA CLI Reference

`lina` is the command; `hermes` is its alias. Run `lina --help` for the installed tree. The table is deliberately a checked subset, not a claim that other commands are absent.

| Command | Purpose | Parser source |
| --- | --- | --- |
| `lina` | Interactive chat | `hermes_cli/main.py`, default dispatch |
| `lina chat --oneshot -q <prompt>` | Answer and exit even on a TTY | `hermes_cli/_parser.py`; wired by `hermes_cli/main.py` |
| `lina -w` | Interactive chat in an isolated worktree | `hermes_cli/_parser.py`; consumed by `hermes_cli/main.py` |
| `lina --continue` / `lina --resume <session>` | Resume recent or selected chat | `hermes_cli/_parser.py`, `hermes_cli/main.py` |
| `lina setup` | Setup wizard | `hermes_cli/subcommands/setup.py` |
| `lina model` | Model/provider picker; select OCX | `hermes_cli/subcommands/model.py` |
| `lina doctor` | Configuration and dependency checks | `hermes_cli/subcommands/doctor.py` |
| `lina config path` / `lina config env-path` | Resolve settings and secrets files | `hermes_cli/subcommands/config.py` |
| `lina config get <key>` / `lina config set <key> <value>` | Inspect/update a setting | `hermes_cli/subcommands/config.py` |
| `lina desk show --json` | Shared Desk path and layout | `hermes_cli/subcommands/desk.py` |
| `lina desk init` | Create the Desk, defaulting to ~/Desk | `hermes_cli/subcommands/desk.py` |
| `lina desk init --path ~/project --adopt` | Adopt an existing folder without deleting files | `hermes_cli/subcommands/desk.py` |
| `lina tools list` | Inspect enabled toolsets | `hermes_cli/subcommands/tools.py` |
| `lina tools enable <name>` / `lina tools disable <name>` | Change next-session tools | `hermes_cli/subcommands/tools.py` |
| `lina skills list` / `lina skills config` | Inventory/platform selection | `hermes_cli/subcommands/skills.py` |
| `lina mcp list` / `lina mcp test <name>` | Inspect/test server connections | `hermes_cli/subcommands/mcp.py` |
| `lina mcp add <name> --command <executable> --args <args...>` | Add stdio server; --args must be last | `hermes_cli/subcommands/mcp.py` |
| `lina mcp add <name> --url <endpoint>` | Add HTTP server | `hermes_cli/subcommands/mcp.py` |
| `lina gateway setup` / `lina gateway run` / `lina gateway status` | Configure, run, inspect messaging | `hermes_cli/subcommands/gateway.py` |
| `lina cron list` / `lina cron status` / `lina cron runs` | Inspect schedules and execution history | `hermes_cli/subcommands/cron.py` |
| `lina webhook list` / `lina webhook test <name>` | Inspect/test event routes | `hermes_cli/subcommands/webhook.py` |
| `lina webhook subscribe <name> --prompt <template>` | Create an event route | `hermes_cli/subcommands/webhook.py` |
| `lina skin list` / `lina skin use <name>` / `lina skin set <key> <hex>` | List, switch, tweak CLI palette | `hermes_cli/subcommands/skin.py` |
| `lina pets --help` / `lina curator --help` | Discover installed optional actions | `hermes_cli/subcommands/pets.py` / `hermes_cli/subcommands/curator.py` |
| `lina logs` / `lina logs gateway` | Inspect agent or gateway logs | `hermes_cli/subcommands/logs.py` |

All listed subcommand builders are wired in `hermes_cli/main.py::_build_cli_parser`. Verify arguments there and in the named parser before adding examples. Plugin commands depend on the active plugin; never present them as built-ins.
