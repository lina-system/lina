# Work panel event latency

T-16 E8 requires 50 actual step transitions with 50 running work cards through
Core, its progress topic and the real TUI in a PTY. The budget is fixed in
`work-panel-latency.json`. The runtime driver remains pending the step progress
projection (#765) and fixed graph evaluation harness (#768).

The measurement support is ready for that driver:

- `panelFrameProbe` arms before a transition and retains the first complete
  PTY renderer write containing its expected card change, with parsed cells and
  the observation time. Split reads cannot satisfy it; coalesced frames cannot
  overwrite the first match.
- `panelLatencySummary` requires all 50 distinct event samples and both times,
  refuses negative intervals, and enforces nearest-rank p95 ≤500 ms and max ≤1 s.
  Boundary and deliberately invalid measurements exercise the gate.

Run the measurement self-tests with:

```sh
go test -count=1 -run 'TestWorkPanel(FirstFrameMeasurement|LatencyBudget)$' ./e2e/m1
```

These self-tests do not establish the runtime latency bound. Completing E8
requires feeding actual Core step commits and their first matching frames from
the fixed corpus into the gate, logging every sample and frame, and retaining
the draft, focus and conversation reading position during the run.
