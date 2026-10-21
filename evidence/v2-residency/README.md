# V2 residency: owner runbook

Part of [#95](https://github.com/thisisjun786/lina/issues/95). The owner chooses
the machines, starts each run and reads the results. No run has happened.
[budget.json](budget.json) was committed before measurement: 64 MiB installed
Core artifacts, 2 seconds to readiness, 256 MiB peak RSS, 1% mean CPU and 5% in
any rolling 60-second window; one logical CPU is 100%. Each run lasts 86,400 seconds.

The [owner-reported pre-run baseline](baseline.json) is a development executable
of 7.3 MiB, socket ready in 27 ms, observed RSS of 6.9 MiB, and no CPU ticks over
30 seconds idle. Platform and source commit were not supplied. This short
observation does not establish protocol readiness or the 24-hour budgets.

Use Linux x64 with systemd and macOS arm64 with launchd. Record the actual OS,
distribution and version: an Ubuntu run is Ubuntu evidence, not Omarchy evidence.
Keep the machines awake and the user sessions alive. Do not use existing
personal state or provider credentials.

## Install a private candidate (both platforms)

Use native packaged artifacts from [#94](https://github.com/thisisjun786/lina/issues/94),
built from the same clean source commit on both machines. Set artifact_dir to
that directory, containing lina, manifest.json and its required engine assets.
Apply #94's verified sqlite-vec configuration before measuring; these service
commands alone do not establish packaging or storage acceptance.
Run the following in one shell, retaining its variables for the chosen platform.

~~~sh
set -eu
umask 077
run_dir=$(mktemp -d /tmp/lina-v2.XXXXXX)
mkdir -p "$run_dir/artifacts" "$run_dir/state/system/core" "$run_dir/state/run" "$run_dir/results"
cp -R "$artifact_dir/." "$run_dir/artifacts/"
install -m 700 "$artifact_dir/lina" "$run_dir/artifacts/lina"
printf '%s\n' '{"main_id":"main-v2-residency"}' > "$run_dir/state/system/core/main.json"
cp "$artifact_dir/manifest.json" "$run_dir/results/manifest.json"
cp evidence/v2-residency/budget.json "$run_dir/results/budget.json"
shasum -a 256 "$run_dir/artifacts/lina" "$run_dir/results/manifest.json" "$run_dir/results/budget.json" > "$run_dir/results/digests.txt"
uname -sm > "$run_dir/results/platform.txt"
~~~

Arm the OS collector and monotonic startup timer before requesting service start.
Measure readiness through the [public protocol used by the harness](../../e2e/README.md):
a correlated hello/welcome, then an empty input refusal with
capability_not_negotiated confirms recovery finished in the current increment.
A welcome or an active service alone is insufficient.

## Linux x64: transient systemd user service

~~~sh
cat /etc/os-release >> "$run_dir/results/platform.txt"
unit="lina-v2-$(basename "$run_dir").service"
systemd-run --user --unit="$unit" --property=Type=exec --property=Restart=no \
  --property=UMask=0077 --property="StandardOutput=append:$run_dir/results/core.log" \
  --property="StandardError=append:$run_dir/results/core.log" \
  "$run_dir/artifacts/lina" core --state-root "$run_dir/state" --socket "$run_dir/state/run/core.sock"
systemctl --user show "$unit" -p MainPID -p ActiveState -p NRestarts
~~~

## macOS arm64: launchd user job

The time wrapper records Core's peak RSS on exit. Sample its Core child, not
the wrapper. The generated job uses a unique label and never restarts Core.

~~~sh
sw_vers >> "$run_dir/results/platform.txt"
label="org.lina.v2.$(basename "$run_dir")"
cat > "$run_dir/core.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!-- SPDX-License-Identifier: Apache-2.0 -->
<plist version="1.0"><dict>
<key>Label</key><string>$label</string>
<key>ProgramArguments</key><array>
<string>/usr/bin/time</string><string>-l</string>
<string>$run_dir/artifacts/lina</string><string>core</string>
<string>--state-root</string><string>$run_dir/state</string>
<string>--socket</string><string>$run_dir/state/run/core.sock</string>
</array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><false/>
<key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>$run_dir/results/core.log</string>
<key>StandardErrorPath</key><string>$run_dir/results/core-time.log</string>
</dict></plist>
EOF
plutil -lint "$run_dir/core.plist"
launchctl bootstrap "gui/$(id -u)" "$run_dir/core.plist"
launchctl print "gui/$(id -u)/$label"
~~~

## Sample and retain results

Write raw samples and startup timing to the private results directory under
run_dir. Every second record elapsed monotonic time, Core PID/start identity,
RSS, cumulative user+system CPU time and service status. On Linux use
/proc/PID/status (VmRSS/VmHWM in KiB) and /proc/PID/stat (CPU ticks divided by
getconf CLK_TCK). On macOS use proc_pid_rusage (resident bytes and CPU nanoseconds);
physical footprint is not RSS. Validate the collector on that OS before the run.

From readiness, probe the protocol every 60 seconds, including both endpoints,
with a two-second deadline and unchanged main identity/epoch. Compute CPU as
100 times CPU-seconds divided by actual elapsed seconds, for rolling
60-second windows at every sample and the complete run. A sample gap over two seconds, failed
probe, restart, suspension or missing peak-RSS measurement prevents a pass.
Record uncompressed sizes and hashes of every required Core artifact.

After 24 hours, stop Linux with systemctl --user stop "$unit". On macOS, send
SIGTERM to the recorded Core child PID; let its time parent exit and write
maximum resident set size (bytes) before launchctl bootout "gui/$(id -u)/$label".
Confirm Core exited and record the exit status. Missing final statistics is
incomplete evidence. Archive the private results before removing the test files:

~~~sh
archive_dir="$HOME/lina-v2-results/$(basename "$run_dir")"
mkdir -p "$HOME/lina-v2-results"
test ! -e "$archive_dir"
cp -R "$run_dir/results" "$archive_dir"
~~~

Sanitize measured summaries into evidence/v2-residency/linux-amd64.json and
evidence/v2-residency/darwin-arm64.json: actual platform, source and budget
commits, artifact digests, UTC interval and continuous duration, sample counts
and gaps, startup/RSS/CPU values, limit comparisons, interruptions and whether
the owner started/read the run. Keep raw logs, private paths and machine names
out of Git. Retain failed attempts; do not relax the budget after measuring.
