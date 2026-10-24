# Apple Foundation Models for short tasks

Research for issue #966. It records what the on-device model does on short tasks
against the person's main model, how the numbers were taken, and what the
numbers do not cover. It is evidence, not a contract. The route is in
[runtime.md](../design/runtime.md#on-device-apple-model).

## Measured, 2026-10-24

Desk Mac (M2 Pro, macOS 27.0.1, on-device model through the helper in
`adapters/applefm`) against the main model available to this host
(`gpt-6-astra`, Responses route). Fixed set of 28 lines: four for each of the
seven short-task kinds. One warm-up call and ten measured calls per line and
path, run sequentially in one process. Accuracy is correct runs over all runs;
percentiles are over the forty calls of the kind, not over four lines.

| Task kind | Lines | Local correct | Main correct | Local p50 | Local p95 | Main p50 | Main p95 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| intent | 4 | 35/40 | 40/40 | 762 ms | 1155 ms | 2310 ms | 4770 ms |
| autocomplete | 4 | 32/40 | 40/40 | 590 ms | 835 ms | 4164 ms | 6443 ms |
| rewrite | 4 | 39/40 | 40/40 | 472 ms | 676 ms | 2355 ms | 4197 ms |
| title | 4 | 10/40 | 40/40 | 2451 ms | 7225 ms | 2870 ms | 4342 ms |
| classify | 4 | 20/40 | 40/40 | 602 ms | 680 ms | 2240 ms | 3325 ms |
| extract | 4 | 40/40 | 40/40 | 1124 ms | 1193 ms | 2598 ms | 3761 ms |
| summary | 4 | 40/40 | 40/40 | 1024 ms | 1178 ms | 2867 ms | 3984 ms |
| all | 28 | 216/280 | 280/280 | 756 ms | 5076 ms | 2712 ms | 5013 ms |

The local p95 is dominated by the two kinds where the model writes more than it
was asked for: a long title drags the whole local distribution out to 5 s. On
every other kind the local model answers in about 0.5–1.2 s, roughly a third to
a fifth of the main model's wait.

## What the misses were

- `title`: the model writes a paragraph where a name was asked for, and its
  answers run to several hundred characters. Two of the four lines never
  produced a short title.
- `classify`: the model labels everything `personal` when the right label is
  `admin` or `health`, although the prompt names all four labels. The two lines
  it gets right are the ones whose answer is `personal` or `work`.
- `autocomplete`: it answers a question back instead of continuing the line, and
  once continued a shopping line with "준비하겠습니다" instead of "보내".
- `intent`: one of four lines (a to-do with a date and time) was read as
  `open_app` in half its runs.
- `extract` and `summary` were correct in every run on both paths. Guided
  generation held the date and time fields exactly.

## How it was taken

The runner is `tools/localmodel-eval`. It drives the same
`internal/core/localmodel.Serve` and the same `kit/provider/applefm` and
`kit/provider/responses` adapters Core uses, so the table measures the shipped
route, not a copy of it. Each case is forced to the path being measured, so both
paths see the identical input and schema.

```sh
# on the desk Mac, in a checkout of this branch
swiftc -parse-as-library -o ~/lina-applefm adapters/applefm/main.swift
go build -o ~/localmodel-eval ./tools/localmodel-eval
~/localmodel-eval -set testdata/localmodel-eval/set.json \
  -helper ~/lina-applefm \
  -main-base http://127.0.0.1:10100/v1 -main-model <main model> -main-key <key> \
  -repeat 10 -warmup 1 -out measurements.json
```

The set is `testdata/localmodel-eval/set.json`. `line` is what the person typed;
`prompt`, when present, is the exact input the caller sends, because a schema of
plain strings has no place to carry the vocabulary a classification or an intent
needs. Dates are absolute so no case depends on the clock. A case is correct
only when every recorded check passes; the checks are listed with the set.

## What the numbers do not cover

- The main-model figures include the network: they leave the Mac for the
  owner's Responses proxy and then the provider. The local figures include the
  helper process start and the model load for that call, which the route pays on
  every call because the helper keeps no session.
- One Mac, one macOS build, one main model, one day. There is no second run on
  another machine and no Private Cloud Compute figure: the public framework
  exposes no third-party Private Cloud session, so that model is unavailable
  and the route falls back, as [runtime.md](../design/runtime.md#on-device-apple-model)
  says.
- The `title` and `classify` misses are quality, not latency. The route has no
  quality gate today: a short task that fits the context is served locally, and
  only a schema miss or an unavailable helper falls back. Whether a length or a
  label-vocabulary check should send those kinds to the main model is an open
  question, not a decision this research takes.
- The helper is Swift and is built by hand on the Mac; no CI job compiles it,
  because Foundation Models needs the macOS 27 SDK, which the CI runners do not
  have. The `contextSize` property it reads is the one the macOS 27 SDK exposes.

## Earlier samples

Before the adapter, the owner ran three launcher lines on the same Mac through a
CLI that wrapped the model's JSON in a code fence. Each figure is one call and
the first includes model load. These are not percentiles, and the adapter uses
guided generation rather than parsing a code fence.

| Task | Line | Result | Correct | Latency |
| --- | --- | --- | --- | --- |
| Intent, structured | 내일 오후 3시까지 RAON 가사 초안 보내기 | to-do title, date 2026-10-24, time 15:00 | yes | 1.94 s |
| Intent | 사파리 열어 | open_app | yes | 0.32 s |
| Intent | 시온한테 CI 왜 깨졌는지 물어봐 | ask_agent | yes | 0.32 s |
