# Dashboard turn overhead

Run from a checkout with the Hermes Python dependencies installed. Linux `/proc`
access to the benchmark's own processes is required for CPU, PSS, and socket
ownership evidence. Set `MEASURE_PYTHON` to the runtime interpreter if it is
different from `python3`. For Rust, put the built `lina-turn-host` binary on `PATH`.

```bash
measure/turn-overhead.sh --impl inproc --sessions 5 --turns 10
measure/turn-overhead.sh --impl python --sessions 5 --turns 10
measure/turn-overhead.sh --impl rust --sessions 5 --turns 10
python3 measure/compare.py out/turn-*.jsonl > out/turn-compare.tsv
```

Each session starts a new `hermes serve` process with a synthetic home, an
ephemeral dashboard port, and an authenticated `/api/ws` connection. All modes
request the `file_read` toolset. The model is an HTTP fixture on `127.0.0.1`.
If this checkout requires a PostgreSQL store, set `MEASURE_STORE_DSN` to an
explicit, disposable loopback database provisioned for the benchmark. Give
concurrent parity or recovery tests a different fixture database.

The driver saves dashboard logs and synthetic state under `out/measure-tmp/`.
After all sessions pass, it atomically publishes one JSONL row per turn to
`out/turn-<impl>.jsonl`. A failed run exits nonzero and preserves any previous
successful JSONL file. Partial wire evidence for failed turns stays in the run's
scratch directory as `failed-turn.json`. Each verified row contains the two model request receipts, the
second request's actual tool-role message with the unique file marker, and the
three WebSocket events. Both model sockets must belong to the selected process.
An isolated host must also be the dashboard's registered direct child.

Latency intervals use client-side monotonic timestamps at submit, tool start,
tool completion, and message completion. Model-excluded elapsed subtracts the
two HTTP handler intervals from request dispatch through response flush. It
includes local HTTP transit and production client parsing. CPU deltas cover
submit through the idle event, using Linux process user and system ticks.

The TSV has one row per mode, metric, and sampling window, with units, sample
counts, median, and nearest-rank p95. Latency and CPU use turns 3–10.
PSS windows are `before_turn_1`, `first_model_request`, `idle_after_turn_1`,
and `idle_after_turn_10`. Idle PSS uses the live process counters after the
post-completion `session.info` reports `running: false`, before the next prompt.
Host PSS is `n/a` before child creation and throughout an in-process run.
