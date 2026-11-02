# LINA Helper on macOS

## Install and permissions

The implementation lives in `helper/`; `hermes_cli/helper_install.py` owns the
`lina install` step. It installs or upgrades the app, probes permissions and
prints which switches a person still needs to enable. A signed build requires
its Developer ID team. Without one, the checkout is built ad-hoc for local use;
those grants do not survive the next ad-hoc rebuild. Use a signed build with the
same identity for durable grants.

1. Run `lina install` using its supported helper build options. Check the command's
   installed-version help rather than inventing deployment flags.
2. A person logged into that Mac's desktop opens System Settings > Privacy &
   Security and enables LINA Helper in **Accessibility** and **Screen & System
   Audio Recording**. These permit AX/input and capture respectively. The agent
   does not click consent dialogs or edit the privacy database.
3. Probe `lina-helperctl permissions` and `lina-helperctl status` with `terminal`.
   The control tool is bundled inside the installed app; user-mode installation
   need not put it on PATH. Use the path reported by the installer if necessary.
   An SSH login alone does not create the required desktop session.
4. Check policy as well as grants. Missing policy defaults to `observe`; invalid
   or unsafe policy restricts the helper to status and permissions. A root-owned
   `operate` policy enables the embedded cua-driver. The helper cannot widen its
   own policy. Collect any policy and grant steps into one clear person task.
5. Check `lina-helperctl cua.status`, then capture a disposable app through the
   real `computer_use` tool. Verify an authorized accessibility action and fresh
   read-back. Inspect the corresponding redacted audit records before claiming
   the full path works. A worker report alone is not acceptance evidence.

## Routing and concurrency

Use the built-in `lina_helper` computer-use provider. Targets come from
`devices.macs`, not an external plugin file. `mac_use` selects the current session's
Mac; choose it before input. Confirm the actual target with status and capture.

The helper launches its bundled driver directly and gates requests with its
policy, caller checks and audit log. Do not start a standalone driver to bypass
that chain. With `observe`, no input driver runs. A "<holder> is using this Mac"
refusal means the input lease belongs to another session: do not race it or
change the holder identity. Read-only checks remain available.

## Troubleshooting

- After a grant change, a process may retain the old TCC result. Use the supported
  restart or installer lifecycle, then recheck permissions and a real capture.
- A shareable display and GUI session must exist. An app with no live window is
  not a failed backend. Use `list_windows` and returned IDs on localized systems.
- Distinguish a locked screen, missing display, denied permission and policy
  refusal. Probe the current state rather than theorizing from another agent's
  behavior. Do not lower lock protection to make a test pass.
- Never run executables from a user-owned app bundle as root. User-mode install
  does not authorize changing root policy or grant databases.
- Keep test input in a disposable window, verify it once, and close test documents
  without saving. Do not test in password managers, terminals or personal dialogs.
- For upgrade, rollback and signing details read `helper/macos/docs/install.md`.
  Preserve the existing team identity and verify grants after a signed update.
