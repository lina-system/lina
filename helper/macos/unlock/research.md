# Design evidence and operating boundaries

Research date: 2026-10-24. Findings were gathered across authorization plugins, headless displays, FileVault/reboot, and FaceTime. This document describes the final package's design constraints; [report.md](report.md) records actual execution.

## Independent implementation

OpenAI documents Locked use as a narrowly scoped Apple authorization-plugin path available only during trusted Computer Use turns. It also includes protective overlays and local-input handling. It is not an external remote-unlock API. The independently implemented package does not call, patch or impersonate OpenAI's service. [OpenAI documentation](https://learn.chatgpt.com/docs/computer-use)

Apple's AuthorizationPlugin API supports mechanisms referenced by authorization-database rules. The package uses a separate screen-unlock right and keeps the entire existing screen rule as its fallback. It does not replace the global `authenticate` rule. [Apple implementation documentation](https://developer.apple.com/documentation/security/extending-authorization-services-with-plug-ins)

## Compatibility risk is an engineering constraint

Apple DTS confirmed a known issue where an authorization plugin can unlock the screen without unlocking the Data Protection Keychain (`FB13128730`). This is why the helper verifies both keychains instead of trusting a successful authorization callback. [Apple DTS answer](https://developer.apple.com/forums/thread/796487)

In the current macOS 27 discussion, Apple DTS recommends against new authorization-plugin implementations and describes the architecture as being in poor condition. The engineer's characterization is not a formal API-deprecation announcement. The source package cannot guarantee that this OS integration will work on every release. [Current Apple DTS guidance](https://developer.apple.com/forums/thread/851093)

Related Codex reports cover SecurityAgentHelper Library Validation failures and keychain state after passwordless unlock. Both were open when checked. They are reports about other environments, not reproduced failures of this package. [Library Validation report](https://github.com/openai/codex/issues/39534), [keychain report](https://github.com/openai/codex/issues/40226)

A separate macOS 27 beta `screenUnlockMode=2` regression was subsequently reported fixed in Beta 5. The package does not apply that legacy setting, and the old beta symptom is not presented as a confirmed current-release bug. [Follow-up](https://developer.apple.com/forums/thread/838847)

## Displays

A physical monitor is not inherently required, but a persistent usable display surface must be verified independently of Screen Sharing. BetterDisplay documents creating and connecting virtual screens; a saved screen configuration is not itself a connected display. [BetterDisplay guide](https://betterdisplay.pro/guide/interface-reference/settings/displays/overview/create-virtual-screen/)

Apple's High Performance Screen Sharing can create virtual displays. Observing such a display while connected does not prove it remains usable after disconnection. [Apple Screen Sharing guide](https://support.apple.com/en-gb/guide/mac-help/mh14066/mac)

This package checks active display count but does not create a virtual display or certify screenshot/AX input. Those are explicit acceptance steps for the Hermes workflow. A dummy HDMI plug is a possible hardware option, not an assumed prerequisite.

## Reboot/FileVault

Apple Silicon Macs running macOS 26 or later can unlock FileVault over SSH after restart if Remote Login and networking are available. The installed `apple_ssh_and_filevault(7)` manual explains that this pre-unlock step uses password authentication; normal SSH keys/settings and shell access are not yet available. [Apple security guide](https://support.apple.com/guide/security/managing-filevault-sec8447f5049/web)

Inference: do not assume a normal Tailscale/user-service path works at that stage. Verify a suitable network path in a separate reboot test. The screen-unlock plugin does not solve this preboot or initial-GUI-login problem.

## FaceTime

Historical `AutoAcceptInvites` defaults are not a current supported integration. An archived author's implementation instead uses UI scripting, and a later developer describes accessibility hierarchy changes between macOS releases. These are implementation references, not evidence of macOS 27 compatibility. [Archived implementation](https://github.com/vrunt/facetime-auto-answer), [developer's UI automation account](https://bensmithgall.com/blog/a-big-button)

Use the actual current incoming-call UI and an authorized test call. Verify accept, authentication and media after a real screen unlock. No FaceTime call was made or answered during package preparation.
