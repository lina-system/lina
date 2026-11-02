# Native MCP Client

LINA discovers configured MCP tools alongside native tools. Inspect existing connections with `lina mcp list` before adding one.

```bash
lina mcp add time --command uvx --args mcp-server-time
lina mcp test time
```

The executable and its dependencies must already be installed. For HTTP transport use `lina mcp add <name> --url <endpoint>`. Check `hermes_cli/subcommands/mcp.py` for supported authentication options. Do not assume serving LINA itself as MCP is a built-in action.

Server settings live under `mcp_servers`. A server uses either a stdio command with arguments or a remote URL, not both. Read back the saved configuration and test discovery. Tool names are normalized to `mcp_{server}_{tool}`; inspect actual results instead of guessing the final name.

- Stdio servers receive a filtered environment. Grant only the credentials that server requires; never forward the entire environment.
- Keep tokens out of examples, tool output, and reports.
- Sampling lets a server request model completions. Disable it for an untrusted server; inspect current sampling limits before enabling it.
- Start a new session when changing the prompt-visible tool list. Do not rebuild an active conversation's schema.

If discovery fails, check executable resolution, transport, authentication, connection timeout, and startup logs. Inspect `tools/mcp_tool*.py` for the installed lifecycle behavior. Do not repair dependencies with raw package installs into the application environment; use the repository's PM workflow.
