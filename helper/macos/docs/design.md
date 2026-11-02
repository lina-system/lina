# LINA Helper design

Status: v0 design, 2026-10-26. Target Mac: agent-mac (macOS 27.0.1, GUI user `agent`, uid 501, (not recorded)).

## 1. Problem

macOS grants privacy permissions (TCC) per app. Today every tool that needs the screen, input or protected files on agent-mac gets its own grant: cua-driver holds Accessibility and Screen Recording, the Hermes Unlock agent holds Accessibility, Screen Sharing holds its own, and each new tool would need another round of clicks in System Settings over Screen Sharing. A new build of an unsigned or ad-hoc tool loses its grant.

LINA Helper is one signed app that holds the grants. Hermes tools, scripts and later LINA ask it to perform a bounded action through a local socket. Grants are given once, to one stable identity, and survive updates and reboots.

Non-goals: MDM or configuration profiles (not used), a generic remote shell, bypassing system privacy prompts, FileVault/initial-login recovery.

## 2. Identity

| Item | Value |
|---|---|
| App | `/Applications/LINA Helper.app` (fixed path) |
| Bundle ID | `app.lina-os.helper` (never changes) |
| Executable | `Contents/MacOS/lina-helper` |
| CLI | `Contents/Helpers/lina-helperctl`, linked from `/usr/local/bin/lina-helperctl` |
| Signing | `Developer ID Application: <Your Name> (<TEAMID>)`, hardened runtime, secure timestamp, only from agent-hub's login keychain |
| Designated requirement | `identifier "app.lina-os.helper" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] and certificate leaf[field.1.2.840.113635.100.6.1.13] and certificate leaf[subject.OU] = "<TEAMID>"` |
| Launch | LaunchAgent `app.lina-os.helper` in the `agent` Aqua session, `KeepAlive`, `LimitLoadToSessionType=Aqua` |

`lina-helperctl` has its own identifier (`app.lina-os.helper.ctl`) and needs no TCC grant: it only forwards requests.

## 3. Permissions

| Permission (System Settings → Privacy & Security) | TCC service | Used for | Milestone |
|---|---|---|---|
| Accessibility | `kTCCServiceAccessibility` | read AX trees, press AX elements, post mouse/key events | v0 |
| Screen & System Audio Recording | `kTCCServiceScreenCapture` | screenshots via ScreenCaptureKit | v0 |
| Full Disk Access | `kTCCServiceSystemPolicyAllFiles` | read allowlisted protected files (e.g. Messages `chat.db`, the screen-capture approval record) | v1 |
| Automation (per target app) | `kTCCServiceAppleEvents` | send Apple Events to allowlisted apps (Messages, FaceTime, System Events) | v1 |

Accessibility, Screen Recording and Full Disk Access are switched on once by a person over Screen Sharing (System Settings → `+` → `/Applications/LINA Helper.app`). Automation is asked per target app the first time the helper sends that app an event; the person clicks Allow once per target. The helper can open the right Settings pane (`permissions --open`) and reports which grants are missing, but it never clicks a privacy dialog or edits a TCC database.

Input Monitoring, Camera and Microphone are not requested. The existing `LinaCallBridge` keeps its own audio permissions.

## 4. Keeping grants

TCC stores the client's bundle ID together with its designated requirement (observed on agent-mac: the `local.hermes.unlock.agent` row carries a 168-byte csreq). The requirement above names the bundle ID and the team, not a code hash, so:

- Rebuilding and re-signing with the same team Developer ID keeps every grant.
- A renewed Developer ID certificate from the same team keeps the grants (same OU). Verify on the first renewal.
- Ad-hoc signing, another team, or a changed bundle ID creates a new TCC identity that must be switched on again. The installer refuses such a bundle.
- The screen-capture approval record (`~/Library/Group Containers/group.com.apple.replayd/ScreenCaptureApprovals.plist`) is keyed by the app's file URL, so the app path stays fixed.

Update procedure (installer `upgrade`): verify the new bundle's signature and designated requirement equal the installed one → `launchctl bootout` the agent → replace the bundle atomically (rename into place) → `launchctl bootstrap` → `lina-helperctl permissions` must report the same grants as before, otherwise the upgrade reports a failure (the previous bundle is kept for rollback).

Reboot: TCC grants persist across reboots. (not recorded) The v0 acceptance test reboots and checks the grants and one request again.

## 5. Screen-recording re-approval

macOS 15 and later periodically remind the user about apps that capture the screen. On agent-mac (macOS 27.0.1) the record for cua-driver shows `kScreenCapturePrivacyHintPolicy = 2592000` (30 days), last alerted 2026-10-23, next hint 2026-11-22. Expect the same 30-day cycle for LINA Helper.

Plan:

1. v0: accept the cycle. The helper reads its own entry in the approval record (needs Full Disk Access, v1; until then the date is computed from the first grant) and `status` reports `screen_recording.next_alert`. The Hermes side warns a few days ahead so a person can answer the alert over Screen Sharing.
2. Observe what happens when the alert is not answered (does capture keep working, stop, or block?). Record the result in `docs/acceptance.md`.
3. Options that remove the cycle, each a decision for the owner:
   - Apple's Persistent Content Capture entitlement (`com.apple.developer.persistent-content-capture`) is meant for VNC-style remote-control apps and is granted by Apple through a request form. It is a request to Apple in the owner's name.
   - MDM (`forceBypassScreenCaptureAlert`) is excluded by design.
   - Editing the approval record by hand is not done: it tampers with system privacy state and a later macOS update may reset it.

The helper never auto-clicks the reminder.

## 6. Request API

Transport: Unix domain stream socket, one request per connection.

- Socket: `~/Library/Application Support/LINA Helper/run/helper.sock`, parent directory `0700` owned by the helper user.
- Framing: one UTF-8 JSON object terminated by `\n`, at most 64 KiB; one JSON response line. 3 s I/O timeout; per-action deadline (default 10 s, screenshot 15 s).
- XPC is not used in v0: Hermes reaches agent-mac over SSH, and an SSH session is outside the GUI session's Mach bootstrap namespace, so a LaunchAgent Mach service is not reachable from it. A file-system socket is. XPC can be added later for a LINA app running in the same GUI session.

Request:

```json
{"v":1,"id":"<uuid>","action":"screenshot","args":{"display":"main"},"caller":"hermes:agent-a"}
```

Response:

```json
{"v":1,"id":"<uuid>","ok":true,"result":{"path":"/Users/agent/Library/Application Support/LINA Helper/outbox/<uuid>.png","width":1920,"height":1080}}
{"v":1,"id":"<uuid>","ok":false,"error":{"code":"permission_missing","detail":"screen_recording"}}
```

Error codes: `bad_request`, `unknown_action`, `not_allowed`, `permission_missing`, `busy`, `timeout`, `target_not_found`, `internal`.

### Authentication

The boundary is "who can run code as `agent` (or root) on agent-mac", i.e. the SSH keys that reach that account. Inside that boundary the helper adds:

1. File-system permission: only the helper user and root can reach the socket.
2. Peer UID check (`getpeereid`): uid must be the helper user or 0.
3. Peer code check (audit token → `SecCode`): the peer must be valid code signed by team `<TEAMID>` (today `lina-helperctl`; later the LINA app). A script cannot talk to the socket directly; it has to go through the CLI, which records who called it (parent process, `SSH_CLIENT`). This is attribution, not a stronger boundary: any process running as `agent` can run the CLI.
4. Policy: `/Library/Application Support/LINA Helper/policy.json`, root-owned `0644`. Lists enabled actions and their limits (file-read roots, Apple Event targets, rate limits). The user-level process cannot widen it. Missing or unreadable policy → only `status` and `permissions` are served.

There are no tokens or passwords, so there is nothing secret to store.

### Actions

| Action | Permission | Args | Result | Milestone |
|---|---|---|---|---|
| `status` | none | – | version, uptime, pid, grants, next screen-recording alert | v0 |
| `permissions` | none | `open` (optional pane name) | each grant true/false | v0 |
| `screenshot` | Screen Recording | `display` (`main` or id) or `window` (window id) | PNG path in outbox, size | v0 |
| `ax.tree` | Accessibility | `app` (bundle ID or `frontmost`), `depth` ≤ 8, `max_nodes` ≤ 2000 | element list with stable paths, role, title, value, frame | v0 |
| `ax.press` | Accessibility | `app`, `path` (from `ax.tree`) | pressed element | v0 |
| `input.click` | Accessibility | `x`, `y` (global points), `button` | – | v0 |
| `input.keys` | Accessibility | `text` or `key` + modifiers | – | v0 |
| `app.open` | none | `bundle_id` | pid | v0 |
| `file.read` | Full Disk Access | `path` under a policy root, `max_bytes` | path of a read-only copy in outbox | v1 |
| `events.send` | Automation | `target` (policy allowlist), `script` (named template in policy) | template result | v1 |
| `unlock.*` | Accessibility + root helper | see §8 | see §8 | v1 |

No action runs a shell command, loads code, or accepts an arbitrary AppleScript.

Screenshots and file copies go to `~/Library/Application Support/LINA Helper/outbox/` (`0700`) and are deleted after 10 minutes. The CLI can stream the file to stdout (`--out -`) so the SSH client copies it without a second connection.

## 7. Audit log

Every request, accepted or refused, appends one JSON line to `~/Library/Logs/LINA Helper/audit-YYYYMMDD.jsonl` (`0600`) and a summary to the unified log (subsystem `app.lina-os.helper`):

```json
{"t":"2026-10-26T06:10:00.123+09:00","id":"…","action":"input.keys","caller":"hermes:agent-a","peer":{"uid":501,"pid":4242,"exe":"/usr/local/bin/lina-helperctl","team":"<TEAMID>","ssh":"100.x.y.z"},"args":{"text_len":12,"text_sha256":"…"},"ok":true,"ms":41}
```

Typed text, file contents and screenshot pixels are never logged; only their length and SHA-256. Files older than 30 days are deleted by the helper at start and daily.

## 8. Absorbing Hermes Unlock

hermes-macos-unlock (imported under `unlock/`, source `thisisjun786/hermes-macos-unlock@e5a3204`) has four parts: an AuthorizationPlugin loaded by SecurityAgentHelper, a root helper `hermes-unlockctl`, a per-user GUI agent app that holds Accessibility and runs the keychain probes, and a transactional installer. Absorption (v1) replaces only the GUI agent:

- LINA Helper serves the agent's root-only socket protocol (`HU_WIRE_MAGIC`, status/trigger/probe) on a second socket under `/var/run/lina-helper/gui-<uid>/`. The root helper keeps verifying that the peer is the installed agent executable by audit-token code hash, now pointing at `/Applications/LINA Helper.app/Contents/MacOS/lina-helper`.
- The Data Protection keychain probe needs `keychain-access-groups` and `com.apple.application-identifier`, which are restricted entitlements: LINA Helper then needs a Developer ID provisioning profile for App ID `<TEAMID>.app.lina-os.helper` (person task, see §11). v0 has no restricted entitlement and no profile.
- The AuthorizationPlugin and the root helper stay separate binaries (the plugin must live in `/Library/Security/SecurityAgentPlugins`; the root helper must run as root via the digest-pinned sudoers rule). They ship inside the app bundle (`Contents/Library/`) and the installer copies them into place.
- The installer's staged install / separate activate / timed rollback / confirm flow is kept unchanged.
- Migration on agent-mac: the current staged Hermes Unlock install is left alone during v0. In v1: `installer.py remove` the old package (it is staged, not active), install the absorbed version, check `status` (Accessibility now comes from LINA Helper), then run the activate → verify-unlock → confirm trial with the owner watching.

## 9. Hermes-side client

Historical (v0/v1 design). In LINA the client is the core package `tools/lina_helper/` and the device list is `devices.macs` in config.yaml; see [fleet.md](fleet.md) §7.

Hermes runs on agent-hub and reaches agent-mac over SSH.

- `clients/lina-helper` (agent-hub script): `lina-helper [--host HOST] ACTION [--arg k=v …] [--out PATH]`. Runs `ssh -T -o BatchMode=yes HOST /usr/local/bin/lina-helperctl …`, a fixed command with JSON-quoted arguments; never a shell string from the caller. Host comes from `--host` or `LINA_HELPER_SSH_HOST`. Screenshots stream back over the same SSH connection to a local path. Exit code 0 only for `ok:true`.
- v0 acceptance uses this script from a Hermes terminal tool.
- v1: a Hermes standalone plugin (`clients/hermes/`) exposing `mac_helper_status`, `mac_screenshot`, `mac_ax_tree`, `mac_ax_press`, `mac_click`, `mac_keys`. The target host is operator configuration, not a model argument. Installing it into a profile is a Hermes configuration change for the operator.
- LINA later talks to the same socket (or XPC when it runs on the same Mac).

cua-driver moves inside the helper as an embedded child that inherits the helper's grants; see `docs/fleet.md` §5. Rollout to the other Macs, the user-mode install and the per-Mac policy profiles are in `docs/fleet.md`.

## 10. Build, test, CI

- Swift 6 (ScreenCaptureKit, AX, AppKit) for the app and CLI; the imported C/Python unlock code stays as is under `unlock/`.
- `make test`: unit tests for protocol parsing, policy, audit redaction, outbox cleanup and request limits run without TCC grants. `make app` builds the bundle (ad-hoc for local checks). `tools/sign.py` signs with the team identity and refuses anything else.
- Builds and signing run on agent-hub only. The private key never leaves agent-hub's login keychain. CI (GitHub `macos-latest`) builds and runs unit tests ad-hoc; it never signs. Private-repo macOS minutes cost 10× Linux minutes, so CI stays one short macOS job plus the Linux unlock tests.
- No secrets in the repository, issues or logs.

## 11. Person tasks

Collected and sent to the owner once:

1. (v0, after the first install) Over Screen Sharing on agent-mac: System Settings → Privacy & Security → Accessibility and Screen & System Audio Recording → `+` → `/Applications/LINA Helper.app` → switch on. About 2 minutes.
2. (v1) developer.apple.com: register App ID `app.lina-os.helper` (macOS, explicit, no capabilities) and a Developer ID provisioning profile for it with the existing Developer ID Application certificate; put the file in agent-hub `~/.config/apple-signing/`.
3. (v1) Full Disk Access switch for LINA Helper; Automation Allow once per target app when first asked.
4. (decision, any time) Whether to request Apple's Persistent Content Capture entitlement to remove the 30-day reminder.
