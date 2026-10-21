# V2 residency measurement

This directory holds the predeclared budget and the measured evidence for
[#95](https://github.com/thisisjun786/lina/issues/95). A procedure, a cross-build,
or a short smoke test does not establish the 24-hour acceptance. Both native
platform results are required. No residency measurement has been recorded yet.

The contract is [runtime acceptance V2](../../docs/design/runtime.md#runtime-acceptance).
Packaging, service startup, and the verified sqlite-vec engine come from
[#94](https://github.com/thisisjun786/lina/issues/94). The storage checks already
recorded in [sqlite-binding.json](../sqlite-binding.json) do not establish
residency of the packaged Core executable.

## Before either run

The [predeclared budget](budget.json) applies to both platforms: at most 64 MiB
of installed Core artifacts, two seconds to protocol readiness, 256 MiB peak
RSS, 1% mean CPU and 5% CPU in each consecutive 60-second window. One logical
CPU is 100%. The workload is an idle service with fresh synthetic state and
periodic health probes, without provider calls. These are targets; neither
platform has a measured result yet.

1. Preserve the numerical budget in the first #95 commit, before building or
   starting either measured candidate. Record that commit
   and the budget file's SHA-256 in each result. Do not increase limits after
   observing a measurement; retain a failure and reopen the runtime choice as
   required by the contract.
2. Merge #94's packaging branch when available and run
   `node scripts/foundation.mjs`. Build both development artifacts from
   the same clean commit using the packaging procedure supplied by #94. Record
   the source commit, Go toolchain, manifest digest, and every required
   artifact's name, byte size, and SHA-256. Confirm the manifest records the
   chosen SQLite binding and verified sqlite-vec engine. Do not publish a
   release to perform this acceptance.
3. Use a real Omarchy Linux x64 machine with a systemd user service and a real
   macOS arm64 machine with a launchd user job. Record OS, OS version,
   architecture, CPU model, logical CPU count, and installed memory, without
   machine names, addresses, usernames, or device identifiers. Neither an
   Ubuntu run nor a macOS cross-build establishes the specified platform run.
4. A person starts each run and reads its outcome. Use dedicated test service
   identities, fresh private state containing only synthetic data, private
   sockets, and no provider credentials or existing installation. Follow #94's
   service templates and installation interface. Do not replace or restart
   another installation. Record the service template digest and any measurement
   overrides, with private paths omitted from public evidence.
5. Keep both machines awake for the entire run. Record the power mode and
   cache conditions before startup. A reboot, suspension, service restart,
   binary replacement, or instrumentation outage makes that attempt incomplete;
   retain it and start a new attempt rather than joining segments.

## Startup and health

Start the timer using a monotonic clock immediately before requesting the
service start. Confirm that systemd or launchd owns the resulting Core process;
a foreground `lina core` process is not the service acceptance. Record process
identity as well as PID so that PID reuse cannot hide a restart.

Connect through the public client protocol using synthetic client and main
identities. Validate the correlated `hello`/`welcome` exchange, negotiated
protocol, main identity, and nonzero epoch. A socket or `welcome` alone does not
mean READY: Core serves handshakes during RECOVERING. With the current
connection-only protocol, a correlated empty `input` request must return
`capability_not_negotiated` with effect state `refused`; only an explicit
`unavailable` permits another startup probe. Stop the startup timer at the
first completed readiness response. If the packaged candidate changes this
interface, agree and record its equivalent readiness observation before either
run; never substitute a sleep or service-manager “active” state.

Begin the continuous 24-hour residency interval after readiness. Repeat the
same protocol health probe with a two-second deadline every 60 seconds, including at the start
and end, and require the same main identity and epoch. Close each probe's
connection so the monitor does not create accumulating clients. Record any
protocol error or missed deadline as a failure, not an omitted sample.

These observations follow the public boundary already exercised by
[the #34 harness](../../e2e/README.md). Cover protocol-observable acceptance
behavior in its own scenario under `e2e/m1/` using that harness, as #95 requires.
Resource measurements themselves use OS instrumentation; a fast protocol
scenario cannot replace their duration or native-platform evidence.

## Resource measurements

Record the instrumentation command or collector source and its digest, version,
units, sampling interval, and clock source with each attempt. Validate the
collector on a disposable process before starting the measured service, so
unit conversion and process-identity handling do not depend on the acceptance
run. Keep monitor resource use separate from Core resource use.

- **Size:** sum the uncompressed bytes of Core's executable and every packaged
  asset needed by this service, including its verified SQLite engine, manifest,
  and service configuration. An engine embedded in the executable is counted
  once. List all terms; do not use a compressed archive or a metadata helper's
  size in place of the installed artifact.
- **Start time:** elapsed monotonic seconds from service-start request to the
  successful readiness response above, including service-manager overhead.
  Record the actual cache conditions; do not label an ordinary start cold.
- **Memory:** sample resident bytes at one-second intervals and retain the OS
  high-water mark from process start, including initialization. Linux exposes
  `VmRSS` and `VmHWM` in `/proc/<pid>/status` in KiB (multiply by 1024). On macOS,
  `proc_pid_rusage` exposes current resident size in bytes, but its lifetime
  maximum physical footprint is a different metric. Obtain Core's lifetime
  peak RSS from `wait4`'s `ru_maxrss` in bytes when a measurement launcher reaps
  that exact Core child after the run. The launcher must belong to the launchd
  test job, forward termination to Core, await its exit, preserve its exit
  status, and write the final statistics before exiting. Validate this on the
  selected macOS version before acceptance; record the launcher as separate
  instrumentation and include its launch overhead in startup timing. Record
  both the sampled maximum and the OS maximum. Do not substitute virtual size,
  physical footprint, or the launcher's RSS. If the collector cannot obtain
  Core's final peak, the result cannot pass a peak-RSS budget.
- **CPU:** sample cumulative process user plus system CPU time, using Linux
  `/proc/<pid>/stat` ticks divided by `getconf CLK_TCK`, or the nanosecond CPU
  counters from macOS `proc_pid_rusage`. Calculate consecutive 60-second windows
  beginning at readiness, including any final partial window, as
  `100 * delta_cpu_seconds / delta_monotonic_seconds`. One logical CPU equals
  100%, independent of the machine's CPU count. Calculate the full-run mean
  from the full counter delta, not an unweighted average of sampled percentages.
  Include health-probe handling in Core's CPU; record startup CPU separately.
- **Continuity:** retain elapsed timestamps, process identity, service-manager
  status, restart counts, and protocol outcomes. A resource sampling gap over
  two seconds prevents acceptance. Explain any late sample and preserve the
  actual interval denominator. Missing counters, backward counters,
  missing health probes, or unaccounted gaps prevent a pass. Unexpected child
  processes require accounting before a result can establish Core's budget.

## Results and decision

After each attempt, stop and remove only its test service and resources. Keep
raw operational records outside the repository. Prepare `linux-amd64.json`
and `darwin-arm64.json` from the actual measurements, with:

- budget commit and digest; source commit, toolchain, manifest and artifact
  provenance; platform and instrumentation details;
- UTC start and end, measured continuous seconds, sample and health-probe
  counts, maximum sampling gap, and any suspension, restart, or interruption;
- artifact bytes, startup seconds, peak resident bytes, full-run CPU percentage,
  maximum 60-second CPU percentage, and a limit comparison for every metric;
- sanitized metric samples sufficient to recompute the aggregates and confirm
  the complete interval, plus the digest of each retained raw record;
- whether a person started and read the run, without publishing their identity;
- all failed or incomplete attempts and the reason for any rerun.

Both platform records must identify the same source commit and predeclared
budget. Require at least 86,400 continuous measured seconds on each, complete
health and resource records, no restart, and every budget comparison passing.
Missing data means incomplete; exceeding a limit means failed. Do not record
zero for an unmeasured value, or claim a pass from extrapolation.

Update V2's design evidence with the observed results and limits of the run.
Run `node scripts/foundation.mjs` again on the final evidence commit. Foundation
checks document integrity; it does not execute or certify the 24-hour runs.
