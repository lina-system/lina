# Sharing a computer with its user

This contract defines how a LINA install proves that it doesn't compete with the person who uses the same machine. It covers input, focus, clipboard, files, CPU and memory, and the session events that change who is in control: lock, unlock, sleep, resume and disconnect. It is normative: an install claims non-competition only through the evidence described here, and any change to the rules is made in this file and joins dev under [integration.md](../policy/integration.md).

Supported platforms come from [product-families.md](product-families.md). Grants, scope enforcement, expiry and reconnect behavior come from [main-authority.md](main-authority.md). The processes LINA runs, and the budget each one declares, come from [runtime.md](runtime.md). This document adds the background load, the acceptance matrix and the pass criteria; it doesn't restate those contracts.

## Scope

Non-competition is the responsibility of every install that puts LINA on a machine a person also uses:

- a Linux main install (`device-os` in the evidence schema): LINA Core on a Linux machine the person uses;
- a desktop install: Node and LINA APP on the person's own Linux, macOS or Windows machine, connected to a main elsewhere.

On such a machine the person has priority. LINA must never take the person's input, focus, clipboard, files or resources away from them. Non-competition and isolation rest on OS permissions and the seat lease, and anything not measured on a real machine is unverified.

A VM install is LINA's own environment. Nobody else works inside the VM, so the non-competition cells of the matrix don't apply to it. The rules that do still apply inside a VM are the ones from [main-authority.md](main-authority.md): authorization before any effect, scope enforcement on files, and the lifecycle rules for grants across lock, sleep and disconnect. When the person wants LINA to act on the VM's host, a Node goes on that host, and the host becomes a desktop install row. What the VM may share with its host is outside this document; see Deferred.

Node is not the owner of non-competition. A Node acts under a grant from the main, and the grant's scope fences what it may touch. The install the Node belongs to proves that LINA yields on that machine.

Mobile devices run LINA APP. LINA APP is an access product, never a permanent Node, so no mobile row exists in the matrix.

## Background load

Every measurement runs with the background load that the machine carries in normal use. It has two parts:

- LINA's own processes on that machine, as listed in [runtime.md](runtime.md): LINA Core, `moirai-worker`, Node, sandboxed tool commands, workers, plugin processes and external components, including background preprocessing of materials.
- LIFE world activity: the background world engine keeping its world, time, events and daily activity.

Agents are data that the one LINA Core runs, with no process of their own ([product-families.md](product-families.md)). The background work of every agent, such as memory extraction from its conversations, runs inside LINA's processes on the main and is part of their load on that machine. Background runs of LINA's sub-agents run inside LINA Core and are part of the same load. Every measurement records how many agents ran background work and how many background runs ran during it.

A measurement that leaves out any part of the background load that the install runs in normal use is not a measurement of that install.

## Seat lease

LINA touches the person's GUI session only under a seat lease: the person's explicit, exclusive and revocable handover of one GUI session to LINA, named in the grant. One task holds a seat lease at a time: the grant names that task, and the lease records the agent that asked for it. Another task, whichever agent asked for it, waits for the lease or gets another session. Without a live seat lease LINA never sends input to the person's session and never takes its focus. Revoking the lease returns the session to the person at once, and a stale grant never acts on a session the person has taken back.

A Windows service runs in Session 0 and never sends input directly to the person's GUI.

The launcher and its clipboard history are features the person turns on, so they are an exception to the focus and clipboard columns on that machine. The launcher takes focus only when the person invokes it, and the clipboard history follows the rules in [surfaces.md](surfaces.md). Every other rule of this document still applies to that machine.

## Work on a locked Mac

A desktop install on macOS can let one task use the person's Mac while its screen is locked. The person turns this on for one Mac, and each task that needs it asks once. LINA unlocks only through its own checked path. It covers every display while the session is unlocked, stops at the person's first input, and locks the Mac again when the task ends. Turning off the lock screen's password is never part of this design and is never suggested to the person. The evidence for this section is in [the locked-Mac research note](../research/locked-mac-computer-use.md).

### Direction

Work on a locked Mac stays off on every Mac until the [eval](../research/locked-mac-computer-use.md#eval) passes on that Mac's macOS build, starting with macOS 27. A failed eval keeps it off on that build. If a hard condition below cannot be met on macOS, screen work moves to a Linux Node with a GUI session of its own (tier 3 of the evidence ladder), and the Mac keeps only its non-screen paths: app actions, the accessibility tree and scripts. Until the eval passes, a task that needs the screen of a locked Mac waits for the person or runs on Linux.

Hard conditions:

- The person's own unlock, by password or Touch ID, is never blocked, and LINA never slows it beyond the bound the eval declares. When any LINA part fails or is missing, the person unlocks through the normal lock screen.
- No LINA part stores, types, reads or passes a password or any other credential. Before it arms a ticket, the broker locks the login keychain and reads that it is locked, and the keychain stays locked during the work. Locking the screen, and a password-less unlock, do not establish that state. Install does not change the person's keychain auto-lock settings.
- An unlock happens only for a task with a live locked-screen lease, and only through a peer whose code signature the plug-in has checked.
- The person's first physical or remote input stops the work and locks the Mac again.

### Parts

- **Unlock plug-in.** An authorization plug-in bundle in `/Library/Security/SecurityAgentPlugins/` with one mechanism. The mechanism runs in the privileged plug-in host, has no interface and answers only allow or deny. Its only state is a crash marker. It connects only to the lock broker of the person who turned the feature on, and it accepts an answer only after the signature check under Unlock.
- **Lock broker.** A small native executable of the Node, signed with LINA's Developer ID team and the hardened runtime, which launchd starts in the person's GUI session. It holds at most one unlock ticket, starts the unlock and locks the Mac again. It takes lease requests only from the Node, after the same kind of signature check.
- **Cover.** A second native process of the Node in the same session. It draws the cover on every display, watches input and stops the work. The broker and the cover watch each other: when one of them exits during a lease, the other locks the Mac.
- **Lock guard.** A root launchd daemon that can only restore the lock screen's authorization rule. It runs at boot and whenever the plug-in folder changes, and it carries the rollback command.
- **Node.** The Node keeps the grant, the lease, the receipts and the input gate of its screen driver ([main-authority.md](main-authority.md#node-states)). It keeps no native code of its own; the parts above are separate signed executables in the Node's artifact. This design registers no protocol kind. The Node issue adds the payloads, generated types and fixtures together with the handlers.

### Install and uninstall

Turning the feature on installs the native parts. macOS asks for an administrator's password in its own dialog, and LINA never sees it.

1. Check the Mac. It must have Apple silicon and a macOS build the eval passed on. The lock screen's rule, `system.login.screensaver`, must be exactly what macOS ships (the rule `use-login-window-ui`) or what an earlier LINA install wrote. Any other rule stops the install and says why: Platform SSO, a smart-card rule, another plug-in or a managed rule. The rule is not touched. Only the eval's test Mac waives the build check, for the install and for the guard, because the eval is how a build becomes measured.
2. Back up the current rule and the macOS build, with a hash of both, into a file only root can read.
3. Copy the plug-in and verify its signature and notarization.
4. Write LINA's unlock right: class `evaluate-mechanisms`, the one privileged mechanism, `tries` 1 and `shared` false. Without `tries` 1, a mechanism rule repeats a denied evaluation up to 10,000 times.
5. Self-test the plug-in through a separate test right that uses the same mechanism, outside the lock screen, and expect a deny within the plug-in's deadline. This proves that macOS loads the plug-in before the lock screen depends on it. A failure removes the bundle and the right and stops the install.
6. Write `system.login.screensaver` as class `rule` with `k-of-n` 1 and the rule list LINA's unlock right, then `use-login-window-ui`. Read it back and compare. A mismatch restores the backup.
7. Ask the person to lock the Mac and unlock it by hand once. The install completes only after that unlock. If no unlock follows within two minutes of the lock, the guard restores the backup, so the next attempt uses the stock rule.

Uninstall runs in an order that never leaves the rule naming a missing plug-in:

1. Turn the feature off, so the plug-in denies at once.
2. Restore the backed-up rule when the current rule is LINA's. When someone else has changed the rule since, leave it alone and report it.
3. Remove LINA's unlock right.
4. Remove the bundle and the state.

The guard restores the backup, or the stock rule when the backup is unusable, and turns the feature off, when it finds any of these: the bundle missing, or its signature invalid, while the rule names it; a rule that differs from what LINA wrote; a macOS build the eval has not passed. The rollback command does the same on request. It needs no Node, no Core and no network, and it runs over SSH or from another administrator account when the person cannot unlock. Turning the feature off in LINA asks the guard to restore. A restore needs no administrator password, because it only returns the Mac to the rule it had.

### Grant flow

- **Turning it on.** The person turns on work on a locked Mac in settings, for one Mac. Core records this standing setting with the person, the Mac's Node, the main id and the date. It is listed with the standing grants and turned off there. The install pins the main id, and the broker refuses a lease from any other main.
- **Each task.** When a step of a task needs the screen of a locked Mac, the agent asks. Core shows one approval request in "Waiting for me" and in the conversation. It names the agent, the task, the apps and the longest time, and says that the screen stays covered, that the login keychain is locked for the lease, and that any key or click locks the Mac again. The choices are once and deny ([main-authority.md](main-authority.md#approval)). No standing grant covers it. The approval is the person's request to lock the login keychain for that lease.
- **The lease.** After the approval, Core issues a grant. It names the Mac's GUI session, a locked-screen lease, the apps the task may drive, and an expiry deadline no later than the approved time. A locked-screen lease is a seat lease of its own kind. An ordinary seat lease still ends when the session locks, and it never turns into a locked-screen lease. The locked-screen lease ends at the first of these: the task ends, its deadline passes, a revoke, a disconnect, an immediate stop, the person's input, sleep, a switch of display or user, or a failure described below. A task that needs more time asks again.
- **The Node.** The Node checks the grant ([main-authority.md](main-authority.md#authorization)) and passes the lease to the broker. Only the broker can arm a ticket.

### Unlock

1. The cover draws its windows on every display while the session is still locked, so the cover is the first thing visible after the unlock.
2. The broker wakes the display and waits, polling a few times a second up to a fixed deadline, until the lock screen is ready for an unlock attempt. A lock after display sleep reaches that state at a different time from a lock from the menu or the screen saver.
3. The broker arms one ticket only while all of these hold: the lease is live, the session is locked, the cover is up on every display, both input watchers run, no input has arrived since the lease began, the stop latch is clear, and the login keychain is locked by the broker's own lock and status read in this attempt ([Keychain and secrets](#keychain-and-secrets)). A ticket is bound to the lease, lives a few seconds at most and is used at most once.
4. The broker submits the lock screen's unlock attempt once and types no characters. While the session is locked, the screen driver refuses all other input, so nothing reaches the lock screen's password field.
5. macOS evaluates the lock screen's rule, and the plug-in runs first. It must answer within its fixed deadline, which the eval sets and which is at most 500 ms. The plug-in:
   - connects to the broker and reads the peer's audit token;
   - checks the peer's running code against the signing requirement built in for LINA's team and the broker's identifier, with the hardened runtime on and no debugging entitlement;
   - checks that the peer runs as the person;
   - sends a fresh nonce;
   - answers allow only when the reply carries that nonce and an armed ticket and the peer's audit token is unchanged.

   Every other case is deny: no broker, a failed check, no ticket, the deadline passing or an internal error. The plug-in never returns an undefined or cancelled result, because either one ends the rule's evaluation before the normal lock screen is tried. A deny moves the evaluation on to `use-login-window-ui`, the person's normal unlock.
6. The broker confirms the unlock within a fixed deadline. Otherwise it withdraws the ticket and sets the stop latch, and the step fails and asks. The Mac stays locked.

The plug-in logs each answer to the system log with the ticket id, and never a credential. It writes a marker when an evaluation starts and clears it when it answers. A marker left by a crashed evaluation makes every later evaluation deny without connecting, until the guard has checked the install.

### Cover and stop

- The cover is a borderless window above every app on each display and on every Space. It holds one fixed sentence: LINA is working on this Mac; press any key or click to stop, and the Mac locks again. It shows no task, agent, app or content. It is not a LINA screen and has no controls.
- The agent's captures leave out only the cover's windows. A display added during the lease is covered before input resumes.
- Two watchers run during the lease. One reads the HID devices: keyboard, pointer and trackpad. The other reads the session's input events, which also carry remote input such as Screen Sharing. Any event that the Node's driver did not post is the person's input.
- At the person's first input, the driver's input gate closes at once and the event is swallowed. The broker locks the Mac, the lease ends with a receipt, the running command goes to CANCEL_REQUESTED ([main-authority.md](main-authority.md#node-states)) and the stop latch is set. LINA sends no input later than 1 s after the person's input, and the Mac is locked within 2 s of it.
- The stop latch refuses every later ticket until the person unlocks by hand. A failed relock, a crash of the cover or the broker, an unexpected answer path in the plug-in, and a login keychain that is not locked during the lease also set it.
- The cover never keeps the person out of an unlocked Mac. While it swallows input, the Mac is being locked. If the lock cannot be confirmed, the cover tells the person to press the Touch ID or power button to lock the Mac.

### Relock

When the lease ends, the broker uses the system's lock command and confirms the locked state within 2 s. The cover stays until the lock is confirmed. If it is not confirmed, the broker tries once more and puts the displays to sleep. The cover stays and keeps swallowing input, the stop latch is set, and the relock is an unknown effect. "Waiting for me" gets a blocking row saying that the Mac may still be unlocked.

Outside a lease, the broker and the cover hold no wake or keep-awake assertion. During a lease, the broker holds a display-wake assertion, and it releases it when the lease ends.

### Keychain and secrets

The locked keychain in this section is the person's login keychain. A password-less unlock does not unlock it, and it does not lock one that is already unlocked. Locking the screen does not lock it either. Locking after a period of inactivity and locking when the computer sleeps are separate Keychain Access settings ([Apple's settings](https://support.apple.com/en-gb/guide/keychain-access/kyca1242/mac)). Install does not read or change those settings, and a setting is not a substitute for the check below.

The approval of the lease is the person's request to lock the login keychain for that work. Before the broker arms a ticket, it locks that keychain and reads its status back, using the calls in [the research note](../research/locked-mac-computer-use.md#the-keychain). Both calls take the login keychain. Passing no keychain would lock and read the default keychain, which is not this check. The keychain counts as locked only when the lock call and the status call both succeed and the unlocked bit is clear. The broker arms the ticket only after that read in the same attempt. If either call fails, or the unlocked bit is set, it arms nothing, the Mac stays locked, the step fails and "Waiting for me" gets a row. The broker stores, types and passes no password, and it does not unlock the keychain or lock any other keychain.

During the lease the broker reads the status on the same cadence as its lock-screen poll. A failed read, or a status whose unlocked bit is set, closes the input gate, locks the Mac and sets the stop latch. A step that needs a keychain item or a password is refused before it runs, and it becomes a row in "Waiting for me" for the person at the Mac. A system authentication window is never a target: the lock screen outside the one submit, an administrator prompt, a keychain prompt. When one appears, the step stops. LINA never turns on secure event input, and the eval checks that none is left on after the work.

### Work panel log

- While the Mac is in use, the task's card says that it is using the locked Mac. The stage list then has one line for each lease: when the Mac was unlocked, when it was locked again, and why (the task ended, a key press, the deadline, a failure).
- Details holds the audit trail: the approval with the person and the agent, the lease and its deadline, each ticket with its time, the keychain lock result and the locked-status read that preceded it, and the plug-in's answer, how long the cover was up on each display, the stop with the kind of input (key, pointer, remote), the time the relock was confirmed and every refused step. Ids appear only in Details ([surfaces.md](surfaces.md#default-view)).
- Unlock and relock are Node effects with receipts. A relock receipt carries the observed locked state. Without it, the relock is unknown and stays a blocking row in "Waiting for me" until the Mac reports that it is locked or the person settles it.
- When the lease ends, the conversation gets one line: the Mac is locked again, or it could not be locked, with the row.

### Acceptance

The [eval](../research/locked-mac-computer-use.md#eval) is the acceptance test. It runs on a test Mac, never on the person's Mac. It passes on a macOS build only when all of these hold in every repetition:

- On a locked Mac, one scripted task finishes, and the Mac is locked again within 2 s of the task's end.
- A physical keypress during the task stops it within 1 s: no input from LINA after that, cancellation requested, and the key never reaches the app.
- A manual password unlock still works after a plug-in crash or hang, after its removal and with no broker running, within the eval's latency bound over the stock unlock.
- Install, uninstall and rollback leave `system.login.screensaver` equal to the backup in every key except its timestamps.
- The runs that start with the login keychain unlocked stay unlocked until the broker locks it. The broker reads the locked status before it arms the ticket, and the keychain stays locked during the lease and after the relock. When that status cannot be established, no ticket is armed and the Mac stays locked. The harness supplies the lock only for the one run that starts locked.

A passing eval is evidence for the lock and unlock column of the macOS row only. Every cell of the matrix stays `unverified until evidence` until its own record exists.

## Matrix

Rows are install modes by host OS. Columns are the eight things that must be measured on a shared machine. Each cell holds exactly one of three values:

- `required`: the rule applies to this cell and must be proven by an evidence record before anything is claimed about it.
- `not applicable`: nobody shares the environment, so there is nothing to measure.
- `unverified until evidence`: the rule applies, but no measurement exists yet. This is the starting value of every shared-machine cell.

| Install mode and host OS | Focus | Keyboard and pointer input | Clipboard | File scope | CPU and memory yield | Lock and unlock | Sleep and resume | Disconnect |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Linux main (`device-os`) | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| desktop on Linux | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| desktop on macOS | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| desktop on Windows | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| VM on Linux host | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable |
| VM on macOS host | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable |
| VM on Windows host | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable |

Rules for reading and changing the matrix:

- No cell ever moves to a success value. A measured cell is reported by its evidence record, which must meet the criteria in the next sections; a policy statement never moves a cell.
- Every shared-machine cell starts as `unverified until evidence`. The Linux main row is measured first because the main runs on Linux. The macOS and Windows rows stay unverified until real evidence exists on those systems.
- A cell must never hold a word that describes a claim without a measurement. Vocabulary outside the three values is a defect in this file.

## Evidence ladder

Evidence is collected in three tiers, in this order:

1. Background CLI. LINA runs as a command-line process with no window, no GUI session in its grant and no clipboard access. The person keeps working in their own session.
2. Dedicated browser profile. LINA drives a browser through a profile that is its own, named in the grant, separate from every profile the person uses.
3. Separated GUI environment. LINA works in a GUI session that is distinct from the person's session, named in the grant, with input and clipboard boundaries measured rather than assumed.

A tier must pass before the next one is attempted. Passing a tier means every applicable matrix column for that row was measured at that tier and met the pass criteria. A failure at a lower tier stops the ladder; the higher tiers stay `unverified until evidence` for that row.

Tiers are per row. Evidence collected on one row says nothing about another.

For Linux tiers 1 and 2, the record checker is
[`scripts/non-competition.mjs`](../../scripts/non-competition.mjs). It checks
one installation and OS version at a time, requires exactly one record for
each of the eight columns at each tier, and rejects tier 2 records unless
every tier 1 cell passed before tier 2 began. Missing cells remain unverified;
a correctly recorded failed cell stops the ladder. Invalid evidence prevents
its tier from reporting a pass even if eight valid cells also exist; a record
whose tier cannot be identified prevents either tier from reporting a pass.
The CLI exits successfully
only when both tiers pass. A headless protocol scenario is supporting evidence,
not a measurement of a person working concurrently on the Linux machine.

## Pass criteria

A measured cell passes only when all of the following are observed during a concurrent session where the person is working, LINA is working and the full background load is running:

- Focus: zero focus steals. The person's active window never loses focus because of LINA, outside a seat lease and outside a launcher invocation by the person.
- Keyboard and pointer input: zero unauthorized input injection. No keystroke, click or pointer movement reaches the person's session unless a live seat lease covers it.
- Clipboard: zero clipboard reads and zero clipboard writes on the person's clipboard, apart from the clipboard history the person turned on.
- File scope: zero out-of-scope file access, and zero lost human edits. Every file LINA touches is in the scope of its grant. When a file changed under the person's hands, LINA's write against the stale revision is rejected; the person's version wins.
- CPU and memory yield: under concurrent load from the person, from LINA and from the background load (LIFE world activity, channel sessions, every agent's background work, agent exchanges and background sub-agent runs included), the person's input latency stays within the declared bounds, and LINA's processes stay within their declared resource bounds. LINA yields when the person needs the machine and recovers its own work afterwards. LINA never kills the person's processes.
- Lock and unlock: the outcome is measured per cell. Locking the person's session must never let LINA act on that session, and unlocking must never let a stale grant resume as if nothing happened. The one exception is a locked-screen lease on macOS, which acts only through its own unlock and relock ([Work on a locked Mac](#work-on-a-locked-mac)). The person's own unlock, a lock by the person and the end of the lease never let a grant act.
- Sleep and resume: the outcome is measured per cell. Resuming from sleep never revives a grant that expired while the machine was asleep, and never lets a stale grant act on the person's session.
- Disconnect: an observed disconnect from the main blocks new work and requests cancellation of running work at once. A silent disconnect ends authorization at the grant's expiry deadline, as defined in [main-authority.md](main-authority.md). In both cases the person's session stays untouched.

Bounds for latency, resource use, yield time and recovery time are declared per test before the test runs and recorded with the evidence. A test without declared bounds is not a measurement. The resident budget each LINA process declares in [runtime.md](runtime.md) is one of the bounds of the CPU and memory yield column.

Zero means zero. One injected key, one stolen focus, one clipboard read or one lost edit fails the cell.

The Linux checker computes measured-minus-bound deltas, including CPU and
resident bytes for every declared process; an excess fails the resources cell.
All cells report the zero-violation counters. File-scope and lifecycle cells
also record that the relevant events were exercised and their required
outcomes observed. An omitted counter, resource bound, process measurement or
required event outcome is not a zero or a pass. Declared bounds predate the
session and trace coverage spans its full duration.

## Non-claims

The following are never accepted as evidence of non-competition on their own:

- A virtual desktop. Being on another desktop doesn't stop input injection, clipboard access or resource pressure.
- A separate profile, session or OS user. Separation is a setup choice, not a measurement.
- A VM. The VM rows are `not applicable`, not passed; a VM proves nothing about a shared machine.
- The word background. A process that calls itself background still has to be measured.
- A process's own limits. A LINA process that caps itself is still part of the measured load.
- Policy text. A sentence that says LINA yields is not a yield. Only a record from a concurrent session counts.
- Research conclusions. Work done during research isn't acceptance on the person's device.

Platform GUI support is unverified until real evidence exists on that platform. This document doesn't claim that any cell has been measured.

## Evidence record

Each measured cell has one record, and it holds:

- the host OS and version, and the install mode
- the session: OS user, GUI session, profile, the grant the work ran under, and any seat lease in force
- the hardware: CPU, memory, and any relevant device
- the versions and release manifest digests of every LINA artifact involved: LINA Core, Node and LINA APP, the Claude adapter when a Claude account served the session, and the optional LINA OS when present
- the workload: what the person did, what LINA did, what LIFE world activity ran, how many agents the main holds, how many of them ran background work and how many background runs ran
- the traces: input, focus, clipboard and file access traces for the whole session, tied to the task and grant ids
- the declared bounds and the measured deltas against them, with resource use reported per process
- the result per column: which pass criteria were met and which were not, against the declared bounds

Records are kept with the acceptance evidence of the implementation that produced them. A record that lacks any of these fields is not evidence.

The Linux schema version 1, template, field units and collection procedure are
in [`evidence/non-competition/`](../../evidence/non-competition/README.md).
Records use stable aliases for private identities and synthetic work content;
sanitized attachments preserve the event counts, timing and task/grant
correlation needed for acceptance without publishing personal content. Each
trace attachment has a relative path and SHA-256 digest, verified by the CLI.
Every LINA artifact category is explicitly versioned or marked absent with a
reason. Additional involved artifacts use the same version/digest fields.
Agent totals, agents doing background work, background runs and the complete
process inventory are separate fields. The checker distinguishes validity of
the record from passing the tier; structural checks and matching hashes do
not authenticate the observer's measurements or establish the completeness of
the declared normal load. Those remain part of manual acceptance.

## Deferred

- Numeric bounds for input latency, resource caps, yield time and recovery time: declared per test and set during implementation acceptance of the Linux main install and of each desktop install.
- The measurements themselves, for every row and tier: set during implementation acceptance.
- The concrete tooling for input, focus and clipboard traces on each host OS: set during implementation acceptance.
- VM sandbox boundaries such as shared folders, clipboard sharing and USB passthrough: set during implementation acceptance of the VM install.
- The order in which the desktop rows are attempted: set during implementation acceptance.
- The deadlines of work on a locked Mac (the plug-in's answer, the wait for the lock screen, the ticket's life, the unlock and relock confirmations) and the latency bound over the stock unlock: declared in the eval before it runs and recorded with its evidence.
