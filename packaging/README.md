# Linux Core installation

Build a clean revision with `node scripts/build.mjs --out <bundle>`. The bundle's
`lina install` creates a new state root and registers a systemd user service.
Installation requires a working user manager and never signs in to a provider.

`--state-root` defaults to `$XDG_STATE_HOME/lina`, or `~/.local/state/lina`.
`--unit-dir` defaults to `$XDG_CONFIG_HOME/systemd/user`, or
`~/.config/systemd/user`. `--bundle` defaults to the running executable's
directory. All supplied locations must be absolute paths. The default private
client socket is `<state-root>/system/run/client.sock`; `--socket` overrides it
when the default would exceed the Unix socket path limit.
The state-root path cannot contain quotes, backslashes or control characters,
which systemd rejects in an executable path. Spaces, dollar signs and percent
signs are supported and escaped according to systemd's argument rules.

The installer checks the build manifest and the SHA-256 and size of every byte
it installs, and requires the manifest target to match the host OS and architecture.
It copies `lina` and `manifest.json` into `system/bin/`, creates the
private filesystem layout, writes a random `system/core/main.json` identity,
and records installation details and tool observations in `system/install.json`
and `system/tools.json`. The service runs the installed executable with explicit
state-root and socket arguments. It is enabled and started without restarting
an existing service.

When the bundle's binary records include `moirai-worker`, its verified bytes
are installed beside `lina`. `lina-moirai.service` depends on Core and starts
the worker with `--canon <root>/canon/canon.sqlite --socket <root>/system/run/moirai.sock`.
The current Core command does not yet connect canon and the job API, so the
installer reports background jobs unavailable and leaves that worker unit
disabled. Its presence is not a readiness claim. Activation belongs to the
increment that connects those Core services.
Without that artifact the installer reports background memory and planning
unavailable and creates no worker service.

The build manifest may also carry:

- `verifiedWorkerVersions`: an object mapping worker names to arrays of verified
  versions, such as `{ "codex": ["0.163.0"] }`. Install compares Codex versions
  after removing the optional `codex-cli ` prefix from both the observed value
  and each listed version; other tools retain exact output-line comparison.
  The original probe output and manifest list remain in `system/tools.json`.
  Older manifests with only `verifiedTools` remain installable: that legacy
  field is read only when `verifiedWorkerVersions` is absent or null. An empty
  canonical map takes precedence over the legacy field. If neither supplies a
  list, no version is verified.
- `installFiles`: an array of `{ "name", "size", "sha256" }` records for
  `adapters/claude/` files and the `adapters/claude-acp/package.json` and
  `adapters/claude-acp/package-lock.json` files in the bundle. A provided adapter must include
  `package.json` and `package-lock.json`. Files are copied to `system/adapters/`;
  installation does not run npm or download the SDK. Builds without those files
  report the Claude component unavailable.

`--tool name=/absolute/path` can be repeated for `codex`, `claude`, `antigravity`,
`cursor-agent`, `gh`, `grok`, `node` and `bwrap`. Other tools are found on `PATH`.
Missing or failed probes are recorded; discovered versions outside the verified
list remain usable and are printed as unverified together with that list.

Any existing state root, unit, unit override, enablement link or socket is a
conflict. Linked destination ancestors are refused, and
socket paths that overlap install destinations are refused as well. A socket
inside the state root must be below `system/run/`; Core owns the other areas.
Before service startup,
failure removes only entries created by this attempt. Once startup is attempted,
the installation is retained on failure because the service may have started;
the command reports failure and leaves the evidence available for diagnosis.
Installation is not an update or repair command.

Service rendering and installation live in
[`internal/core/install`](../internal/core/install/install.go). The
[runtime contract](../docs/design/runtime.md#the-lina-command) governs product
behavior.

The macOS parts of a desktop install — the unlock plug-in, the lock guard, the
installer and the rollback command — are packaged separately; see
[the macOS package](macos/README.md).
