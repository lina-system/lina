# Acceptance record

Live results on the target Mac. Exact commands and outputs; no screenshots are committed.

## v0 (milestone "LINA Helper v0", issue #5)

Target: The agent's Mac (macOS 27.0.1, GUI user `<user>`, (not recorded)).

### 2026-10-26 06:32 KST — first install (before grants)

Build of PR #13 (merged), signed on the hub Mac with `tools/sign.py --team TEAMID`:
`Identifier=app.lina-os.helper`, `flags=0x10000(runtime)`, timestamp present, `TeamIdentifier=TEAMID`.

On the agent's Mac:

```
sudo /usr/bin/python3 tools/install.py install --user <user> --team TEAMID --app "$PWD/LINA Helper.app"
installed 0.1.0
sudo /usr/bin/python3 tools/install.py status --user <user>
  installed: true, agent_loaded: true,
  requirement: identifier "app.lina-os.helper" and anchor apple generic and ... certificate leaf[subject.OU] = TEAMID
  permissions: {"accessibility": false, "screen_recording": false}
```

`/Library/Application Support/LINA Helper/policy.json` is `root:wheel 0644`.

From the hub Mac (`LINA_HELPER_SSH_HOST=<host> clients/lina-helper ...`):

| Request | Result |
|---|---|
| `status` | `ok:true`, grants both false |
| `screenshot --out ...` | `permission_missing` / `screen_recording`, exit 1, no file written |
| `ax.tree --arg app=frontmost` | `permission_missing` / `accessibility` |
| `input.keys --arg 'text=$(id)'` | `permission_missing` / `accessibility`; audit shows `text_len` 5 and SHA-256 only |

Audit lines carry `peer.exe=/Applications/LINA Helper.app/Contents/Helpers/lina-helperctl`, `team=TEAMID`, and the SSH origin.

### 2026-10-26 06:55 KST — grants switched on

Accessibility and Screen & System Audio Recording switched on once for `/Applications/LINA Helper.app` (person, over Screen Sharing). TCC rows on the agent's Mac: `kTCCServiceAccessibility|app.lina-os.helper|2`, `kTCCServiceScreenCapture|app.lina-os.helper|2`.

From the hub Mac: `screenshot --out ...` → `ok:true`, PNG.

### 2026-10-26 06:57–07:01 KST — upgrade keeps grants; live defects fixed (#15, PR #16)

- `install.py upgrade` with a rebuilt, re-signed bundle: both grants still true afterwards (TCC rows unchanged).
- The live run found four defects, fixed in PR #16: `ax.tree` failed on Finder (AXError -25200), upgrade bootstrapped before launchd dropped the old agent, the installed bundle kept the staging owner, the client could not pass typed arguments.
- After the fix, from the hub Mac:
  - `ax.tree --arg app=com.apple.finder --json '{"depth":3}'` → 40 nodes, menu bar items `0/1/0` Apple … `0/1/7` 도움말.
  - `ax.press --arg app=com.apple.finder --arg path=0/1/4 --arg expect_role=AXMenuBarItem` → `ok:true`; a screenshot shows the 보기 (View) menu open.
  - `input.keys --arg key=escape` → `ok:true`; the next screenshot is byte-identical to the one taken before the press.

### 2026-10-26 07:02 KST — restart

The agent's Mac restarted from the command line, nobody touching it. (not recorded)

From the hub Mac about 3 minutes later:

```
status      → ok:true, grants {"accessibility": true, "screen_recording": true}, uptime 165 s
screenshot  → ok:true
ax.tree (Finder, depth 2) → ok:true, 19 nodes
```

Other services on the agent's Mac also came back.

### 2026-10-26 07:3x KST — main build installed

Build of `main` at 29684f9 signed on the hub Mac and installed with `upgrade`; `permissions` → both true.

## v0 result

All exit conditions met: installed on the agent's Mac, grants given once and kept across a restart and repeated upgrades, Hermes on the hub Mac served screenshot, accessibility tree, accessibility press and key input.

## Embedded cua-driver (#20) — the agent's Mac, 2026-10-26 08:43 KST

Build of `feat/20-embedded-cua` signed on the hub Mac (outer app team TEAMID; `Contents/Helpers/cua-driver` keeps its upstream signature, team YCK386LBJ7, cdhash `b067c86e…`), installed with `upgrade`. Grants stayed true. Root policy switched from the v0 explicit actions form to `{"profile":"operate"}` (same actions, plus the cua child); the v0 file is kept on the agent's Mac for rollback.

- `cua.status` under `operate`: child running, `check_permissions` → `accessibility: true`, `screen_recording: true`, `source.attribution: host`, `host_bundle_id: app.lina-os.helper`, `responsible_ppid` = helper pid. The policy file is gone from `run/` once the socket is up.
- Over the helper's socket: `get_screen_size`; `list_apps` ok; `get_window_state` on Finder → 76 elements and a PNG; `clipboard_read` → "not allowed by the YAML policy".
- TCC (`/Library/Application Support/com.apple.TCC/TCC.db`): no new `com.trycua.driver` row (the two existing rows belong to the standalone 0.21.0 driver). One new row `kTCCServiceSystemPolicyAllFiles|app.lina-os.helper|0` (denied, auth_reason 5) appeared at child start: something in the child probed a protected file and the request was charged to the helper. It grants nothing; the helper shows up unticked under Full Disk Access.
- `{"profile":"observe"}`: within 7 s no child, no `cua.sock`, `cua.status` says "not running under observe", `input.keys` → `not_allowed`. Back to `operate`: child restarted within 12 s.
- Native checks on the hub Mac (`LINA_CUA_NATIVE_TESTS=1`): upstream binary passes while suspended; an ad-hoc re-signed copy is rejected before it runs; a copy with a modified executed page is killed by the kernel.
