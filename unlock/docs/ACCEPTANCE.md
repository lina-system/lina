# Acceptance matrix

The package's isolated tests and the deployment's live acceptance are separate evidence.

| Check | Delivered evidence | Live target acceptance |
|---|---|---|
| Grant expiration, replay, competing consumers | Linux + macOS tests pass | Confirm no repeated unlock from one request |
| User/session/boot binding | Isolated tests pass | Switch/logout/relogin must refuse stale requests |
| GUI peer UID + audit-token code hash | Native macOS test passes | Installed LaunchAgent must connect with correct identity |
| Trigger, receipt, session observation, postcheck orchestration | Synthetic end-to-end flow passes | Real loginwindow integration not yet exercised |
| Native AuthorizationPlugin ABI | Build, ordinary-host load, synthetic callbacks pass | SecurityAgentHelper loading not yet verified |
| Installer rollback and original-rule preservation | Mocked authorization backend tests pass | Trial deadline/reboot recovery not yet exercised |
| Both keychains | Noninteractive probe implemented | Current development baseline fails: legacy -25293, DP -34018 |
| GUI Accessibility | Development-process query true | Installed LaunchAgent grant unverified |
| CUA without physical monitor or Screen Sharing | Existing driver/virtual-display observations only | Capture and input must work after both are disconnected |
| FaceTime | No calls placed or accepted | Test authorized incoming call, accept UI, media and authentication |
| Initial login/FileVault | Not implemented by this package | Separate recovery mechanism required |

For live testing, include repeated lock/unlock cycles, expiration, a canceled request, a stopped GUI agent, a mismatched user, and manual password/Touch ID fallback. A successful return from a test harness is never substituted for this matrix.

Keychain error codes are observations, not proof of a single cause. The ad-hoc agent lacks a production signing identity; DP -34018 indicates a missing entitlement in the observed execution. Legacy -25293 is an authentication failure and must be diagnosed without resetting or exposing the user's existing credentials. Activation rejects this baseline.
