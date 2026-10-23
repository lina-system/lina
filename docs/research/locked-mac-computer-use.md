# Research: computer use on a locked Mac

Research for [#974](https://github.com/lina-system/lina/issues/974), read on 2026-10-23. The owner wants computer use on their Mac to keep working while the screen is locked, without ever turning off the lock screen's password. The questions: how macOS lets software other than the lock screen authorize an unlock, how that software can prove who it is talking to, how the Mac is locked again, how the person's input is noticed, what fails today on macOS 26 and 27, and what open-source drivers do. This note is the evidence for [Work on a locked Mac](../design/non-competition.md#work-on-a-locked-mac) and for the approval rule in [main-authority.md](../design/main-authority.md#approval). It is not normative.

No Mac was touched for this note. Nothing was installed, and no command ran on macOS. The sources are Apple's documentation, Apple's published source code, Apple's developer forums and public issue trackers. The owner's read-only observations of their own Mac are in the #974 comments.

## Findings

1. The lock screen authorizes an unlock through the authorization right `system.login.screensaver`. macOS ships it as a `rule` that points to `use-login-window-ui`. An authorization plug-in takes part only when the right leads to a rule of class `evaluate-mechanisms` that names one of the plug-in's mechanisms. One shape is known to work on current releases: a rule list of a plug-in rule followed by `use-login-window-ui`, with `k-of-n` 1, so that either branch can unlock.
2. In a rule list, only a plain deny moves on to the next rule. An internal error or a cancel ends the whole evaluation. A crashed plug-in host, a bundle that macOS cannot load and a mechanism that answers "undefined" all produce an internal error. A plug-in placed before the normal lock screen must therefore answer deny on every failure path it controls, and the rule must never name a plug-in that is missing.
3. The authorization engine waits for a mechanism's answer with no time limit. A plug-in that waits on a socket stalls every unlock behind it, including the person's own. Public reports show 3 to 5 s added to every unlock and one stall of 17.5 minutes. The deadline has to be inside the plug-in.
4. A mechanism rule repeats a denied evaluation up to its `tries`, which defaults to 10,000. LINA's rule sets `tries` to 1.
5. The step that starts the evaluation on a locked session is inside `loginwindow`, which is closed source. Public reports describe a service that waits for the lock screen's password field, submits it, and lets its plug-in answer the evaluation that follows. The same reports show the weak points of that step. After display sleep the service gave up 20 ms before the field appeared. In another case a fallback typed text into the password field.
6. A plug-in can identify its socket peer by audit token. `LOCAL_PEERTOKEN` returns the audit token of the process that last used the peer socket, looked up when the option is read. Security.framework turns that token into a code object that can be checked against a signing requirement. Because the lookup happens at read time, the plug-in reads the token again after the answer arrives and compares the two.
7. macOS documents no call that locks the screen at once. The supported ways are the person's own commands, such as Control-Command-Q and Lock Screen in the Apple menu. The lock state is not a documented key either. A relock is therefore an action whose result is observed. When the result cannot be observed, the relock is reported as unknown.
8. The person's input can be watched through two documented paths: the HID devices, under the Input Monitoring permission, and event taps, where each event carries the process id of its source. Remote input from Screen Sharing comes through the session's event stream, not from a HID device. One report shows a service that ignored it. LINA watches both paths.
9. A password-less unlock does not unlock the login keychain. The engine marks every `system.login.*` evaluation as needing the keybag to be unlocked, and only a password can do that. The same unlock does not lock a keychain that is already unlocked, and locking the screen does not lock it either. Lock after inactivity and lock when sleeping are separate Keychain Access settings. `SecKeychainLock` locks a keychain without a password, and `SecKeychainGetStatus` reports whether it is unlocked (`kSecUnlockStateStatus` means unlocked). Both calls are deprecated. Tasks must not depend on keychain items, and a ticket has to wait until the login keychain has been locked and that status has been read.
10. No open-source driver unlocks a locked Mac today. The driver that the owner's assistant uses refuses background input behind the lock screen, and a fix closed on 2026-10-19 makes it report the lock as a lock. Its plan to unlock with a plug-in is still open.
11. Direction: design the feature on macOS, but keep it off until the eval below passes on the macOS build, and fall back to Linux if it cannot pass. See Direction.

## Sources

| What | Revision or date | How it was read |
| --- | --- | --- |
| Apple documentation: [Authorization Plug-ins](https://developer.apple.com/documentation/security/authorization-plug-ins), [Extending authorization services with plug-ins](https://developer.apple.com/documentation/security/extending-authorization-services-with-plug-ins), [AuthorizationCallbacks](https://developer.apple.com/documentation/security/authorizationcallbacks), [AuthorizationPluginInterface](https://developer.apple.com/documentation/security/authorizationplugininterface) | Live pages, 2026-10-23 | Documentation JSON, read as text |
| Apple documentation: [SecCodeCopyGuestWithAttributes](https://developer.apple.com/documentation/security/seccodecopyguestwithattributes(_:_:_:_:)), [kSecGuestAttributeAudit](https://developer.apple.com/documentation/security/kSecGuestAttributeAudit), [SecRequirementCreateWithString](https://developer.apple.com/documentation/security/secrequirementcreatewithstring(_:_:_:)), [SecCodeCheckValidity](https://developer.apple.com/documentation/security/seccodecheckvalidity(_:_:_:)), [SecCodeCopySigningInformation](https://developer.apple.com/documentation/security/seccodecopysigninginformation(_:_:_:)), [kSecCodeInfoFlags](https://developer.apple.com/documentation/security/kseccodeinfoflags), [kSecCodeInfoEntitlementsDict](https://developer.apple.com/documentation/security/kseccodeinfoentitlementsdict), [xpc_connection_set_peer_code_signing_requirement](https://developer.apple.com/documentation/xpc/xpc_connection_set_peer_code_signing_requirement(_:_:)), [NSXPCConnection.setCodeSigningRequirement](https://developer.apple.com/documentation/foundation/nsxpcconnection/setcodesigningrequirement(_:)) | Live pages, 2026-10-23 | Same |
| Apple documentation: [IOHIDRequestType](https://developer.apple.com/documentation/iokit/iohidrequesttype), [CGPreflightListenEventAccess](https://developer.apple.com/documentation/coregraphics/cgpreflightlisteneventaccess()), [CGEvent.tapCreate](https://developer.apple.com/documentation/coregraphics/cgevent/tapcreate(tap:place:options:eventsofinterest:callback:userinfo:)), [CGEventTapOptions.listenOnly](https://developer.apple.com/documentation/coregraphics/cgeventtapoptions/listenonly), [CGEventField.eventSourceUnixProcessID](https://developer.apple.com/documentation/coregraphics/cgeventfield/eventsourceunixprocessid), [CGEventSource.secondsSinceLastEventType](https://developer.apple.com/documentation/coregraphics/cgeventsource/secondssincelasteventtype(_:eventtype:)), [CGSessionCopyCurrentDictionary](https://developer.apple.com/documentation/coregraphics/cgsessioncopycurrentdictionary()), [window server session properties](https://developer.apple.com/documentation/coregraphics/window-server-session-properties), [SCContentFilter(display:excludingWindows:)](https://developer.apple.com/documentation/screencapturekit/sccontentfilter/init(display:excludingwindows:)), [NSWindow.Level.screenSaver](https://developer.apple.com/documentation/appkit/nswindow/level-swift.struct/screensaver), [canJoinAllSpaces](https://developer.apple.com/documentation/appkit/nswindow/collectionbehavior-swift.struct/canjoinallspaces), [DistributedNotificationCenter](https://developer.apple.com/documentation/foundation/distributednotificationcenter) | Live pages, 2026-10-23 | Same |
| Apple support: [Lock the screen of your Mac](https://support.apple.com/guide/mac-help/lock-the-screen-of-your-mac-mchl8e8b6a34/mac), [Mac keyboard shortcuts](https://support.apple.com/en-us/102650) | Live pages, 2026-10-23 | Search summary |
| Apple support: [If your Mac keeps asking for your keychain password](https://support.apple.com/en-gb/guide/keychain-access/kyca1242/mac) | Live page, 2026-10-23, macOS Tahoe 26 guide | Page text |
| Apple documentation: [SecKeychainLock](https://developer.apple.com/documentation/security/seckeychainlock(_:)), [SecKeychainGetStatus](https://developer.apple.com/documentation/security/seckeychaingetstatus(_:_:)), [kSecUnlockStateStatus](https://developer.apple.com/documentation/security/ksecunlockstatestatus) | Live pages, 2026-10-23. `SecKeychain` deprecated in macOS 10.10 | Documentation JSON, read as text |
| Apple's authorization engine, [Security-61901.120.67](https://github.com/apple-oss-distributions/Security/tree/Security-61901.120.67/OSX/authd): `engine.m`, `rule.c`, `agent.c`, `authorization.plist`, and `OSX/libsecurity_authorization/lib/AuthorizationTagsPriv.h` | Tag `Security-61901.120.67`, commit `cbb0aed4e0f3c12f87862fd16d04fe5846d90c4f` | Source read |
| Apple's kernel, [xnu](https://github.com/apple-oss-distributions/xnu/tree/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea): `bsd/sys/un.h`, `bsd/kern/uipc_usrreq.c` | Commit `f6217f891ac0bb64f3d375211650a4c1ff8ca1ea` | Source read |
| Apple Developer Forums, [thread 737268](https://developer.apple.com/forums/thread/737268): authorization plug-ins at screen unlock on macOS 14 and later, with answers from Apple Developer Technical Support | Read 2026-10-23, last post April 2026 | Page summary |
| cua-driver tracker, [trycua/cua](https://github.com/trycua/cua): [#1743](https://github.com/trycua/cua/issues/1743), [#1744](https://github.com/trycua/cua/issues/1744), [#1745](https://github.com/trycua/cua/issues/1745), [#3064](https://github.com/trycua/cua/issues/3064), [#5482](https://github.com/trycua/cua/issues/5482) | Read 2026-10-23; `main` at `27d8da9fd94056bd73892183531579396aafb2f9` | Issue text |
| Reports R1 to R15: public issue reports against a commercial computer-use service that unlocks a locked Mac with an authorization plug-in | Filed 2026-05-22 to 2026-10-05, read 2026-10-23 | Issue text. The list with links is in a comment on #974. |

## The lock screen's authorization

### The plug-in interface

An authorization plug-in is a bundle in `/Library/Security/SecurityAgentPlugins/`. Its entry point, `AuthorizationPluginCreate`, receives the engine's callbacks (`AuthorizationCallbacks`) and returns the plug-in's interface (`AuthorizationPluginInterface`: `MechanismCreate`, `MechanismInvoke`, `MechanismDeactivate`, `MechanismDestroy` and `PluginDestroy`). A mechanism reports its decision through `SetResult` with one of four results: allow, deny, undefined (failed and not to be retried in this session) or user cancelled. The callbacks also read and write hints and context values, and read the arguments, the session id and the `LAContext`.

Apple's article names two plug-in hosts. One runs as an anonymous user and can show an interface. The other runs as root and must not connect to the window server. A mechanism runs in the root host when its name ends in `,privileged`, for example `Plugin:mechanism,privileged`. An installer adds a rule with `AuthorizationRightSet`. Apple notes that the key names for rule definitions live in `AuthorizationTagsPriv.h`, which is not public API. The command-line form is `security authorizationdb read <right>` and `security authorizationdb write <right>`.

### The shipped rules

In `authorization.plist` of Security-61901.120.67:

| Name | Definition |
| --- | --- |
| `system.login.screensaver` (right) | class `rule`, rule `use-login-window-ui`, version 1. The comment says to set the rule to `authenticate-session-owner-or-admin` to use the SecurityAgent instead. |
| `use-login-window-ui` (rule) | class `user`, group `admin`, `session-owner` true, `shared` false, `allow-root` false |
| `authenticate-session-owner-or-admin` (rule) | The same keys as `use-login-window-ui` |
| `system.login.screensaver.unlock` (right) | class `evaluate-mechanisms`, mechanism `CryptoTokenKit:login`, `tries` 1, comment "Do not modify" |

`use-login-window-ui` and `authenticate-session-owner-or-admin` have the same keys. The difference is in how `loginwindow` treats the name, and that code is not published. Apple Developer Technical Support calls the plug-in API "super brittle" and ask plug-in vendors to test every beta (forum thread 737268). The same thread shows that macOS 14.0 left the desktop visible when a plug-in handled screen unlock, fixed in 14.1. For plug-ins that draw their own unlock view, Apple recommends `defaults write /Library/Preferences/com.apple.loginwindow screenUnlockMode -int 2`. In April 2026 a developer reported that on macOS 26.4.1 a plug-in's lock screen appeared after the stock one, so the lock screen showed twice. The values of `screenUnlockMode` are not documented. LINA's plug-in has no view, so LINA does not set it.

A Mac that uses Platform SSO ships a different rule. One report (R1) shows `system.login.screensaver` pointing to `psso-screensaver`, a `k-of-n` 2 list of a Platform SSO mechanism branch and `authenticate-session-owner-or-admin`. An installer that replaced that rule broke password unlock on that Mac.

### How a rule list is evaluated

`_evaluate_class_rule` in `engine.m` tries the rules of a list in order. It stops with success once `k-of-n` rules have succeeded. It moves on after a failure only when the failure is `errAuthorizationDenied`. It stops at once on `errAuthorizationCanceled` or `errAuthorizationInternal`. The mechanism results map onto these as follows (the end of `_evaluate_mechanisms` in `engine.m`; `agent_run` in `agent.c`):

| What the mechanism does | Engine status | In a `k-of-n` 1 list |
| --- | --- | --- |
| Answers allow | success | The list succeeds and later rules are not tried |
| Answers deny | denied | The next rule is tried, after the mechanism rule's own tries |
| Answers undefined | internal error | The evaluation ends and later rules are not tried |
| Answers user cancelled | cancelled | The evaluation ends |
| Its host crashes during the call | internal error: the status stays undefined | The evaluation ends |
| Its bundle cannot be loaded | internal error ("Error creating mechanism agent") | The evaluation ends |
| Never answers | `agent_run` waits with `DISPATCH_TIME_FOREVER` | The unlock stalls |

A rule of class `evaluate-mechanisms` takes `tries` from its definition, with a default of `kMaximumAuthorizationTries`, 10,000 (`rule.c`). The engine re-runs its mechanisms while the status is denied and tries remain. A rule of class `rule` takes `k-of-n` from its definition, with a default of 0, meaning every rule must succeed.

What `loginwindow` does after an internal error is not visible in the source. Report R2 is consistent with the table. A plug-in rule placed before `use-login-window-ui` broke local password unlock until the stock rule was restored with `security authorizationdb write system.login.screensaver use-login-window-ui`, while login after a restart still worked.

### Which rule goes first

The design keeps LINA's rule first and the normal lock screen second, as in the shape known to work. The other order was considered:

- With `use-login-window-ui` first, the person's successful unlock would never reach LINA's code, so a crash, a hang or a missing bundle could not touch it.
- But the agent's own unlock attempt would then begin as a failed password check. That may count toward the lock screen's failed-attempt delays, which would slow the person's next unlock.
- And while a ticket was armed, a wrong password typed at the Mac would unlock it.

With LINA's rule first, the risks to the person's unlock are a plug-in that hangs, crashes or is missing. The design answers each one. The deadline is inside the plug-in. A crash marker makes later evaluations deny without connecting. A self-test at install proves that the plug-in loads. The guard restores the rule when the bundle disappears. Uninstall restores the rule before it removes the bundle. Case E3 of the eval measures the person's unlock in each failure. If E3 fails on a build, the other order is measured next. If neither order passes, the fallback in Direction applies.

### Starting the evaluation

Nothing in Apple's documentation says how software starts an unlock evaluation for a locked session. The reports describe the pattern in use:

- the service wakes the display;
- it waits for `loginwindow` to expose its password field;
- it submits the field;
- its plug-in, first in the rule list, answers the evaluation that follows.

Report R8 gives the timing of one failure on a macOS 27 beta. After a lock from display sleep, the service began checking the accessibility tree 7 ms after the display woke, found no field and gave up 29 ms later. The field appeared 20 ms after that. After a direct lock, the same flow worked. Report R10 shows the cost of an unguarded fallback: text that was meant for an app went into the lock screen's password field, and the task reported success. In the design, the broker waits for the field up to a deadline, submits once without typing, and the driver refuses every other input while the session is locked.

### The keychain

`engine.m` sets the hint `AGENT_HINT_KBGUNLOCK_REQ` for every right whose name starts with `system.login.`, so the evaluation is expected to unlock the user's keybag. A password-less allow provides nothing to unlock it with. This matches the owner's observation in #974 that the login keychain stays locked after such an unlock when it was locked beforehand.

A login keychain that is already unlocked is a different case. The screen lock does not lock it. Keychain Access locks it only when the person has turned on "Lock after" a number of minutes or "Lock when sleeping" ([If your Mac keeps asking for your keychain password](https://support.apple.com/en-gb/guide/keychain-access/kyca1242/mac)). Both settings are off unless the person turned them on, and neither is the screen lock. `security show-keychain-info` prints those settings. It does not print whether the keychain is locked now.

[`SecKeychainLock`](https://developer.apple.com/documentation/security/seckeychainlock(_:)) locks a keychain and takes no password. Passing no keychain locks the default keychain, which is not necessarily the login keychain. [`SecKeychainGetStatus`](https://developer.apple.com/documentation/security/seckeychaingetstatus(_:_:)) reads the status of the keychain it is given, and [`kSecUnlockStateStatus`](https://developer.apple.com/documentation/security/ksecunlockstatestatus) means unlocked. Apple notes that the status can change at any time, and that `SecKeychainLock` is for when the person has asked to lock the keychain. Both calls belong to `SecKeychain`, which Apple marks deprecated since macOS 10.10. No replacement lock call appears in this note. A missing keychain returns `errSecNoSuchKeychain`. An invalid one returns `errSecInvalidKeychain`.

The design has the broker lock the login keychain and read that status before it arms a ticket, leave the auto-lock settings unchanged, and keep reading the status during the lease. The broker makes those calls while the screen is locked. Whether they succeed in that state was not measured, and neither was whether the data protection keychain stays readable afterwards. The eval's test item is in the login keychain, and the eval is what shows that these calls still lock it on a measured build.

## Proving the peer

`bsd/sys/un.h` defines the local-socket options `LOCAL_PEERCRED`, `LOCAL_PEERPID`, `LOCAL_PEEREPID`, `LOCAL_PEERUUID`, `LOCAL_PEEREUUID` and `LOCAL_PEERTOKEN` ("retrieve peer audit token"), all at level `SOL_LOCAL`. In `bsd/kern/uipc_usrreq.c`, reading `LOCAL_PEERTOKEN` takes the peer socket's `last_pid` at that moment, finds the process with that pid and returns its task's audit token. Two things follow:

- The token belongs to the process that last used the peer socket when the option is read, not necessarily the process that accepted the connection. A peer that handed its socket to another process would be read as that other process. The plug-in reads the token at connect and again after the answer arrives, and accepts the answer only when both match.
- The check is made on the audit token, never on a pid that is looked up again later.

[SecCodeCopyGuestWithAttributes](https://developer.apple.com/documentation/security/seccodecopyguestwithattributes(_:_:_:_:)) with the attribute `kSecGuestAttributeAudit` returns a code object for the token. `SecCodeCheckValidity` checks that the running code is valid and meets a requirement compiled with `SecRequirementCreateWithString`, for example an Apple-issued Developer ID chain, LINA's team identifier and the broker's code identifier. `SecCodeCopySigningInformation` returns the code's flags (`kSecCodeInfoFlags`), which show the hardened runtime, and its entitlements (`kSecCodeInfoEntitlementsDict`), where the plug-in refuses `com.apple.security.get-task-allow`, the entitlement that lets a debugger attach.

XPC offers the same check through `xpc_connection_set_peer_code_signing_requirement` (macOS 12) and `NSXPCConnection.setCodeSigningRequirement` (macOS 13). A mechanism in the root plug-in host would have to reach a service in the person's GUI session. Whether it can do that through XPC was not tested, so the design uses a local socket. The socket lives in a folder that only root and the person can write, so another account cannot take its name. A process of the person's own account could still bind a socket at that name, but it would fail the signature check, so the worst it can do is make LINA's unlock fail.

The open-source plan (#1745) puts the socket in a root-owned folder (`0600`, owner `root`, group `_securityagent`), reaches it through a helper installed with sudo, and checks task tokens with an HMAC key provisioned at install. LINA checks the peer's code signature instead, so there is no shared key to provision, store or leak.

## Relocking

Apple's support pages list the ways to lock a Mac: Control-Command-Q, Lock Screen in the Apple menu, the Touch ID button, the Lock Screen key on some keyboards, and a hot corner. None of them is a programming interface. The broker uses the same command the person would use, and then observes the result.

The lock state has no documented key. `CGSessionCopyCurrentDictionary` documents five keys: user id, user name, console set, on console and login done. None of them reports the lock. Drivers read an undocumented key, `CGSSessionScreenIsLocked`, and listen for the undocumented distributed notifications `com.apple.screenIsLocked` and `com.apple.screenIsUnlocked` (#1743, #5482). The design treats these as observations to cross-check, not as contracts. A relock is confirmed only when they agree, and otherwise it is unknown.

#5482 adds two facts, observed on macOS 26.6. When the screen saver starts, the session goes behind the lock screen at once, even with the password requirement set to never. Behind the lock screen, every app's `AXWindows` comes back empty while window captures still work. So a capture of a locked session can look normal, but input goes to the lock screen. Declaring user activity (`caffeinate -u`) did not end that state; a real click did.

## Watching for the person

- **HID devices.** `IOHIDRequestType` (macOS 10.15) names the listen-event access that HID monitoring needs, the Input Monitoring permission. A HID watcher sees keyboard, pointer and trackpad events from devices. Events that software posts do not come from a device.
- **Event taps.** `CGEvent.tapCreate` places a tap where HID events enter the window server (`cghidEventTap`). A listen-only tap needs the same permission, checked with `CGPreflightListenEventAccess` (macOS 10.15). Each event carries the process id of its source in `eventSourceUnixProcessID`, which separates the Node driver's own events from all others. A tap that can drop events needs the Accessibility permission. The cover uses one so that the person's first event never reaches an app.
- **A third signal.** `CGEventSource.secondsSinceLastEventType` with the HID system state reads the time since the last hardware event, as a cross-check on the two watchers.
- **Remote input.** Report R11: Screen Sharing input did not stop a service whose cover said "press any key or click", which left a headless Mac with no recovery path. In the design, any event the driver did not post counts as the person's input, wherever it came from.
- **Secure event input.** Report R13: after an unlock attempt, secure event input stayed on, owned by a process that had already exited, until the person logged out. It broke event-tap tools in ordinary apps. LINA never turns secure event input on, and the eval checks that it is off afterwards.
- **Not known.** Whether the HID watcher sees keys while the lock screen holds secure event input for its password field. Case E2 includes a key pressed before the unlock to find out.

## Failure modes on current releases

Reports R1 to R15 are about one commercial service that uses the plug-in shape in finding 1. The causes are as reported; none was reproduced for this note.

| Id | macOS | What happened | Reported cause | What the design does |
| --- | --- | --- | --- | --- |
| R1 | 27.0 | Password unlock failed on a Platform SSO Mac; Touch ID still worked | The installer replaced the Platform SSO rule instead of keeping it | The install refuses any rule but the stock one; uninstall restores the backup |
| R2 | 26.5 | Local password unlock failed until the stock rule was restored | A plug-in rule before `use-login-window-ui` | The plug-in denies on every failure path it controls; the guard; eval E3 |
| R3 | 26.5.1 | Every unlock, by Touch ID or password, took 3 to 5 s longer | Waiting in the plug-in path | A deadline inside the plug-in; the eval's latency bound |
| R4 | 26.5.1 | One unlock stalled for 17.5 minutes after the right password | Waiting in the plug-in path | The same |
| R5 | 26.5 | The plug-in was registered but never loaded | Library validation rejected it | The self-test at install; the guard checks the signature |
| R6 | 27.0 beta | Automatic unlock failed; a manual unlock worked | Unknown | The feature stays off until the eval passes on the build |
| R7 | 27.0 | Automatic unlock failed again after a service restart | Unknown | The same; a failed unlock leaves the Mac locked and asks |
| R8 | 27.0 beta | After display sleep, the service gave up 20 ms before the password field appeared | One check instead of a wait | Wait up to a deadline; the eval covers each kind of lock |
| R9 | 26.6 | Failed after an idle display lock; worked after a manual lock | Unknown | The same |
| R10 | 26.5 | On a turn from a phone, a fallback typed text into the lock screen's password field and reported success | An unguarded fallback | The driver refuses all input while locked except the one submit; a failed check stops and asks |
| R11 | 26.5.2 | Screen Sharing input did not stop the work | Only HID input counted | Any input the driver did not post stops the work |
| R12 | 26.2 | The cover appeared during a task that used only the shell and ignored a local click; the Mac became unresponsive | Unknown | The cover exists only during a lease; the stop does not depend on the cover's interface; the cover never keeps the person out without a lock |
| R13 | 26.6.1 | Secure event input stayed on until logout | Unknown | Never turned on; the eval checks it |
| R14 | Not stated | The locked display woke every 10 minutes | A user-activity assertion outside the work | No assertion outside a lease; the eval checks it |
| R15 | Several | The feature could not be turned on on Intel Macs | Missing or unsigned Intel parts | Apple silicon only |

The #974 comments list four risks: automatic unlock failing on macOS 27 (R6, R7, R8), failures on turns started from a phone (R10), the plug-in blocking the person's own unlock (R1, R2, with R3 and R4 slowing it), and the locked keychain (see The keychain).

## Open-source drivers today

- **cua-driver** ([trycua/cua](https://github.com/trycua/cua)), the driver behind the owner's assistant. Behind the lock screen it refuses background input, because the target window is missing from `AXWindows` and input would go to the lock screen. Before the fix for #5482 (closed 2026-10-19) it reported that as "another Space" and suggested foreground delivery, which would also have sent input to the lock screen. The fix makes it report the lock. Its `desktop_unlocked` field is the session's capture scope, not the lock state (#3064). Nothing keeps the display awake during its work.
- **Its unlock plan.** #1743 (a lock observer and a display-sleep guard) and #1744 (an authorization plug-in) were folded into #1745, which is open. The plan has a plug-in, a cover on every display, a relock on HID input, task tokens checked with an HMAC, a root-owned socket and a helper installed with sudo. Its non-goals include typing the password and automating second factors. LINA's design differs in four places. It checks the peer's code signature instead of a shared key. It asks for approval for each task through Core. It counts remote input as the person's. It adds the guard, the install self-test and the automatic restore when the person cannot unlock.
- **Other drivers** were not surveyed for this note.

## Direction

LINA designs work on a locked Mac on macOS. The feature stays off until the eval passes on the Mac's macOS build, and if it cannot pass, LINA falls back to Linux. The reasons:

- The mechanism exists and needs no stored password. The person's unlock stays the stock lock screen, and LINA's branch only answers allow or deny.
- The reported failures come either from choices the design avoids (replacing the rule, unbounded waits, no load check, ignoring remote input, unguarded fallback input) or from macOS 27 builds that nobody has measured yet.
- What cannot be read from documentation or source is exactly what the eval measures before anything ships: how `loginwindow` handles an internal error and the rule order, how an evaluation starts after each kind of lock, and whether the HID watcher sees keys behind the lock screen.
- If the eval cannot pass on macOS, screen work runs on a Linux Node with a GUI session of its own, and the Mac keeps app actions, the accessibility tree and scripts. The owner's requirement holds either way, and the lock screen's password stays on.

## Eval

This plan was written before any building, so that implementation is measured against it. It runs on a test Mac, never on the person's Mac, and it was not run for this note. The eval harness issue turns it into a script. Each case writes one evidence record with the fields of [the evidence record](../design/non-competition.md#evidence-record), plus the case's measurements, the declared bounds and the deltas.

### Setup

- **Test Mac.** Apple silicon, the current macOS 27 release, recorded with `sw_vers`. A test account with a test password and administrator rights; the password is a test fixture and never the person's. The lock screen requires the password immediately. No MDM enrollment, no Platform SSO. Whether the Mac has Touch ID is recorded.
- **Controller.** A Linux host that runs a test LINA main, the eval script, SSH to the test account with a key (the recovery path), and a VNC client for Screen Sharing input.
- **Physical input.** A USB HID emulator: a microcontroller that presents a keyboard and a mouse to the Mac and takes commands from the controller over serial. The controller records when it sends each event.
- **Builds.** The release build of every part, plus a test variant of the plug-in that reads two switches from a root-only folder: crash during the call, and hang during the call. A test variant of the broker reads one switch from that folder: the keychain lock call fails. The release plug-in and the release broker have no switches. A test variant is never left installed.
- **Keychain.** The login keychain holds one test item created before the runs. The harness reads lock status with `SecKeychainGetStatus` on that keychain. `kSecUnlockStateStatus` means unlocked. The harness does not treat `security show-keychain-info` as a status, because that command prints the auto-lock settings. It runs `security lock-keychain` only in the one E1 run that starts from a locked keychain.
- **Clock.** The offset between the controller and the Mac is taken from three SSH round trips before each case. Every time bound below includes the measured uncertainty.
- **Declared bounds**, fixed before the first run:

| Measure | Bound |
| --- | --- |
| The plug-in's answer | at most 500 ms |
| The broker's wait for the lock screen | at most 5 s |
| A ticket's life | at most 3 s |
| The unlock confirmation | within 3 s of the submit |
| Stop: the last input LINA posts | at most 1 s after the person's input |
| Stop: cancellation requested at Core | at most 1 s after the person's input |
| Stop: the Mac locked | at most 2 s after the person's input |
| Relock at the end of the task | at most 2 s |
| Manual unlock latency with LINA installed | median at most the stock median plus 200 ms; 95th percentile at most the stock 95th percentile plus 300 ms |

- **Baseline.** Before LINA is installed: 20 manual unlocks, with the HID emulator typing the test password and Return. Latency runs from Return to the observed unlock.

### The scripted task

Open TextEdit, create a document, type the line `lina-eval <run id>`, save it as `~/lina-eval/<run id>.txt` and quit TextEdit. The check is that the file holds exactly that line.

### Cases

**E1. A locked Mac finishes one task and is locked again.** Ten runs for each kind of lock: Control-Command-Q from the HID emulator, idle display sleep (display sleep set to one minute) and the screen saver starting. Each run:

1. Unlock the login keychain with the test fixture. Read its status and require `kSecUnlockStateStatus`. The harness does not run `security lock-keychain` on these runs.
2. Lock the Mac and wait until the lock is observed. Read the status again and require the unlocked bit still set, so the screen lock did not establish the locked keychain.
3. The test owner approves a locked-screen lease.
4. Core dispatches the task. Wait for the task to end.

The run passes when all of these hold:

- The file is correct.
- The Mac was unlocked only between the ticket's use and the relock.
- The cover's log shows it on every display for that whole time, and a capture of the full display that the harness takes as root in the session, cover included, matches the log.
- The Mac was locked within 2 s of the task's end.
- Unlock and relock have receipts.
- `pmset -g assertions` lists no LINA assertion afterwards.
- `ioreg -l -w 0` shows no `kCGSSessionSecureInputPID` afterwards.
- The broker's log shows a lock of the login keychain and then a status read of that same keychain with the unlocked bit clear, both before the ticket is armed. The harness did not lock it.
- A read of the login-keychain test item fails during the lease and after the relock, and the status stays locked through the relock.

Two further runs use a Control-Command-Q lock. In one, the harness locks the login keychain before the screen lock and does not unlock it; the broker still locks that keychain and reads the locked status before the ticket. In the other, the broker's test variant makes the lock call fail; no ticket is armed, the Mac stays locked, the task asks, and the unlocked bit is still set.

**E2. A keypress stops the task within 1 s.** Twenty runs for each input: a key and a click from the HID emulator, a pointer move from the HID emulator, and a key through Screen Sharing from the controller. The input is sent at t0, during the typing step. A run passes when:

- the driver's log shows no LINA input after t0 + 1 s;
- Core records the cancel request by t0 + 1 s;
- the Mac is locked by t0 + 2 s;
- the TextEdit document holds no stray key;
- the next approved lease cannot unlock (the stop latch);
- after a manual unlock, a new lease unlocks again.

One more run per input kind presses a key while the lock screen is still up, before the submit. It passes when no ticket is armed. It also records whether the HID watcher saw the key behind the lock screen.

**E3. A manual password unlock still works when LINA's parts fail.** Ten runs per failure, with the HID emulator typing the test password and Return:

- (a) installed, no lease;
- (b) the test plug-in crashes during the call;
- (c) the test plug-in hangs during the call;
- (d) the bundle is deleted over SSH while the rule still names it (a broken uninstall), measured before and after the guard reacts;
- (e) the broker is not running;
- (f) the Node and Core are stopped.

A run passes when every unlock succeeds within the latency bound. In (b), later evaluations also deny without connecting. In (d), the run records whether `loginwindow` fell through to the normal lock screen before the guard restored the rule, and the guard restores the rule within 2 s. On a test Mac with Touch ID, an operator repeats (a) to (f) with a finger. Those results are recorded separately as operator-assisted.

**E4. Install, uninstall and rollback.** Each check passes as stated:

- (a) Install on the stock rule: the rule reads as designed. Uninstall: the rule equals the backup in every key except its timestamps, and the bundle and LINA's right are gone.
- (b) Install on a test rule shaped like the Platform SSO rule in R1: the install is refused and the rule is unchanged.
- (c) Install an unsigned test bundle: the self-test fails, the install stops and nothing is left behind.
- (d) Install, lock, then wait two minutes without unlocking: the guard restores the rule, and a manual unlock works through the stock rule.
- (e) The rollback command over SSH while the Mac is locked: a manual unlock works afterwards.
- (f) Remove the current build from the list of measured builds: the guard turns the feature off and restores the rule.

**E5. The lease ends in each other way.** Run each of these during a lease: the deadline passing, a revoke from Core, Core stopping, the network dropping, and sleep (`pmset sleepnow`). In each case the Mac is locked within 2 s; after sleep it is locked on wake. No later unlock happens without a new approval.

### Script outline

```sh
lina-eval locked-mac preflight --mac <host> --hid <serial port>   # sw_vers, the rule and its hash, the baseline
lina-eval locked-mac install   --mac <host>                        # E4 (a) install and its checks
lina-eval locked-mac run E1 E2 E3 E4 E5 --repeat <n>               # writes one evidence record per case
lina-eval locked-mac restore   --mac <host>                        # the rollback command; the rule must equal the preflight hash
```

Each record keeps these raw logs:

- the Node's log and the logs of the broker and the cover, including the keychain lock result and the status read;
- the harness status reads before the screen lock and during the lease;
- the plug-in's lines from the system log (`log show` with the plug-in's subsystem);
- `security authorizationdb read system.login.screensaver` before and after;
- the HID send times.

Cleanup always runs `restore`, compares the rule with the preflight hash and removes the test variants of the plug-in and the broker.

### What the eval decides

- A build where every case passes may turn the feature on.
- If E1 or E2 fails, the feature stays off on that build until a fix passes.
- If E3 fails, the other rule order is measured. If that order fails E3 too, the feature is off on macOS, and screen work goes to Linux as in Direction.

## Not checked

- Nothing in this note ran on a Mac.
- Whether locking the login keychain also locks the data protection keychain was not measured. The note names no replacement for the deprecated `SecKeychain` lock and status calls.
- `loginwindow` is closed source. How it handles an internal error from the rule, and the rule order, are not verified.
- Whether a mechanism in the root plug-in host can reach an XPC service in the person's GUI session was not tried.
- Whether the HID watcher sees keys behind the lock screen is not known.
- The commercial service's design beyond the rule names in #974 and the public reports.
- Open-source drivers other than cua-driver.
- macOS 27 releases after 27.0.1.
