# Sharing a computer with its user

This contract defines how a LINA install proves that it doesn't compete with the person who uses the same machine. It covers input, focus, clipboard, files, CPU and memory, and the session events that change who is in control: lock, unlock, sleep, resume and disconnect. It is normative: an install claims non-competition only through the evidence described here, and any change to the rules is made in this file and joins dev under [integration.md](../policy/integration.md).

The installation modes come from [product-families.md](product-families.md). Grants, scope enforcement, expiry and reconnect behavior come from [main-authority.md](main-authority.md). The processes LINA runs, and the budget each one declares, come from [runtime.md](runtime.md). This document adds the background load, the acceptance matrix and the pass criteria; it doesn't restate those contracts.

## Scope

Non-competition is the responsibility of every install that puts LINA on a machine a person also uses:

- the device-OS install: LINA OS on a machine the person uses;
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

## Matrix

Rows are install modes by host OS. Columns are the eight things that must be measured on a shared machine. Each cell holds exactly one of three values:

- `required`: the rule applies to this cell and must be proven by an evidence record before anything is claimed about it.
- `not applicable`: nobody shares the environment, so there is nothing to measure.
- `unverified until evidence`: the rule applies, but no measurement exists yet. This is the starting value of every shared-machine cell.

| Install mode and host OS | Focus | Keyboard and pointer input | Clipboard | File scope | CPU and memory yield | Lock and unlock | Sleep and resume | Disconnect |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| device-OS: LINA OS | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| desktop on Linux | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| desktop on macOS | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| desktop on Windows | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence | unverified until evidence |
| VM on Linux host | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable |
| VM on macOS host | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable |
| VM on Windows host | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable | not applicable |

Rules for reading and changing the matrix:

- No cell ever moves to a success value. A measured cell is reported by its evidence record, which must meet the criteria in the next sections; a policy statement never moves a cell.
- Every shared-machine cell starts as `unverified until evidence`. The LINA OS row is measured first because the main runs LINA OS. The macOS and Windows rows stay unverified until real evidence exists on those systems.
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
- Lock and unlock: the outcome is measured per cell. Locking the person's session must never let LINA act on that session, and unlocking must never let a stale grant resume as if nothing happened.
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
- the versions and release manifest digests of every LINA artifact involved: LINA Core, Node, LINA APP and LINA OS, and the Claude adapter when a Claude account served the session
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

- Numeric bounds for input latency, resource caps, yield time and recovery time: declared per test and set during implementation acceptance of LINA OS and of each desktop install.
- The measurements themselves, for every row and tier: set during implementation acceptance.
- The concrete tooling for input, focus and clipboard traces on each host OS: set during implementation acceptance.
- VM sandbox boundaries such as shared folders, clipboard sharing and USB passthrough: set during implementation acceptance of the VM install.
- The order in which the desktop rows are attempted: set during implementation acceptance.
