# Signing a runner in on a node from the hub (lina#230)

Each runner keeps its own sign-in, and the node only checks and reports it
([lina-runners.md](lina-runners.md)): Grok and Antigravity (agy) through their
own CLI, Cursor with `CURSOR_API_KEY`, Claude and Codex (and their ACP adapters)
through the node's magpie. Accounts are signed in on the hub's magpie. For Grok and agy the hub can do the sign-in in one step:

```sh
lina devices sign-in <node> <runner> [--json]
```

This starts the runner's own sign-in on the node and finishes it in the hub's
browser, where the owner is already signed in. Without arguments,
`lina devices sign-in` stays the read-only list of runners waiting for a sign-in.
The device screen's sign-in button (#234) calls the same function,
`hermes_cli.lina_sign_in.start(node, runner)`.

The result has `node`, `runner`, `state` (`signed_in`, `needs_person` or `failed`)
and a one-line `reason`. When a person is needed it also has `need` and
`question`, the id of the inbox question. The command exits 0 only for
`signed_in`.

## The flow

1. The hub reads the node's `hello` and finds the runner's row. Runners that sign
   in through magpie or a key are refused with `reported only: ...`, and their
   current state is returned. A runner that is already signed in is left alone.
   A runner LINA has not installed on the node (no shim) is refused with the
   `lina runners install` to run there.
2. Over ssh, the node starts the CLI through LINA's shim with `BROWSER` set to a
   helper that writes the sign-in address into a 0600 file in a 0700 temporary
   folder. The script hands the address back over the same ssh channel, so the
   hub keeps it in memory only. The CLI's own output goes to `/dev/null` on the
   node.
   - Grok: `grok login --oauth`, a loopback sign-in. Its address returns to
     `http://127.0.0.1:<port>/...` on the node.
   - agy: `agy` without arguments, under a terminal that a small python3 `pty`
     helper opens on the node, on every OS. macOS's BSD `script` cannot run with
     a fifo on stdin. Without python3, a Linux node falls back to
     `script -qfec`, and a macOS node is refused with "no pty helper on the
     node". Enter picks the
     first entry of its menu, Google OAuth. Its address returns to a page on the
     provider's site that shows a code to paste. There is no local return.
3. For a loopback return, the hub opens `ssh -L <port>:127.0.0.1:<port> <node>`,
   so `127.0.0.1:<port>` on the hub reaches the CLI's own listener on the node.
   A browser often does not follow a page's redirect to 127.0.0.1 on its own (a
   site may need the browser's local-network permission for that). So the browser
   command may hand the return address back instead: the hub checks that it is
   `http://127.0.0.1:<port>/...` on the tunnel's port and sends it one GET
   through the tunnel, without a proxy, and drops the answer. The address stays
   in the hub's memory, and the token is made by the CLI and never passes
   through LINA. For agy, the browser command prints
   the code from the return page. The hub keeps it in memory and types it into
   the CLI over the ssh channel; it is never written to a file.
4. The hub runs the browser command (below). Then it waits until the runner's
   credential file exists on the node, or until the CLI ends.
5. The hub closes the tunnel and asks the node session to stop, then ends the
   session if it has not ended within 10 s. It then runs a separate short stop
   call on the node. That call stops every process the session recorded and all
   their descendants (a CLI under the pty helper included), and removes the folder and
   the node's runner report cache (`<LINA root>/worker/runners.report.json`).
   Its `cleaned` is the confirmation; without it the result is `failed`. The hub
   then reads `hello` again and returns `signed_in` only when the node now
   reports the runner signed in.

Only one sign-in of the same runner on the same node runs at a time. The lock
file is `<LINA root>/worker/hub/sign-in/<node>.<runner>.lock`.

## The hub's browser command

```yaml
worker:
  sign_in:
    browser:
      command: [/path/to/hub-browser-sign-in]   # the hub operator's own; not shipped with LINA
    accounts:
      grok: <account to choose>
      agy: <account to choose>
```

The command gets the sign-in address on **stdin**, never in argv. Its environment
is the hub's child-process environment with Hermes' secrets scrubbed
(`build_subprocess_env(scrub_secrets=True)`): provider keys and bot, GitHub and
remote-compute tokens are dropped, while PATH, HOME (`HERMES_REAL_HOME` when HOME
is redirected), DISPLAY and SSH_AUTH_SOCK pass. It also gets these variables:

| Variable | Value |
|---|---|
| `LINA_SIGN_IN_RUNNER` | the runner (`grok`, `agy`) |
| `LINA_SIGN_IN_ACCOUNT` | `worker.sign_in.accounts.<runner>`, or empty |
| `LINA_SIGN_IN_RETURN` | `address` (a return to `127.0.0.1:<port>`: print the return address on stdout, or follow it and print nothing) or `code` (print the code the return page shows on stdout) |

The command opens the address in the hub's signed-in browser profile and clicks
through the account choice and consent:

- exit 0: consent is given. For `address`, the first line on stdout is the
  return address the provider sent the browser to (`http://127.0.0.1:<port>/...`),
  stopped before the browser loads it. If stdout is empty, the browser has
  followed the return itself and the hub sends nothing. The hub refuses any other
  host or port. For `code`, the code is on stdout.
- exit 3 with one stderr line `needs_person: two_step`, `needs_person: new_device`
  or `needs_person: account_choice`: a person is needed. Use `account_choice`
  when the account in `LINA_SIGN_IN_ACCOUNT` is empty or not listed.
- any other exit: the sign-in failed.

LINA never shows the command's output. Without `worker.sign_in.browser.command`,
nothing is started and the reason says so.

## When a person is needed

The flow stops: the CLI on the node is stopped and the tunnel closed. One inbox
question is then written into the work record that the board's inbox reads. It
is an open owner's to-do whose decision names the node, the runner and what is
needed, for example "Sign in to grok on build-a: the sign-in asks for a two-step
check." While that question is open, a second stop for the same node and runner
returns the same question instead of writing a new one.

The question is written with `lina work`'s own `ledger.append` under the work
lock, not through `work add`. The writing rules of `work add` refuse device
names, and this question has to name the node.

## What is never shown

No sign-in address, code or token appears in standard output or errors, logs,
results, the inbox question or a card. The node's address file is removed with
its folder. The only node detail the hub passes to ssh is the return port, in
the `-L` argument.
