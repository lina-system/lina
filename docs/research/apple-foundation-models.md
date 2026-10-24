# Apple Foundation Models for short tasks

Research for issue #966. It records the on-device samples the owner measured
before this adapter, and what this host could not measure. It is evidence, not
a contract. The route is in [runtime.md](../design/runtime.md#on-device-apple-model).

## Samples

The owner ran three launcher lines on the desk Mac (M2 Pro, macOS 27) through
a CLI that wrapped the model's JSON in a code fence. Each figure is one call.
The first call includes model load. These are not percentiles.

| Task | Line | Result | Correct | Latency |
| --- | --- | --- | --- | --- |
| Intent, structured | 내일 오후 3시까지 RAON 가사 초안 보내기 | to-do title, date 2026-10-24, time 15:00 | yes | 1.94 s |
| Intent | 사파리 열어 | open_app | yes | 0.32 s |
| Intent | 시온한테 CI 왜 깨졌는지 물어봐 | ask_agent | yes | 0.32 s |

The adapter uses guided generation for a schema. It does not parse a code fence
from the on-device model.

## Not measured here

This working host is Linux. It cannot load Apple Foundation Models, accept the
framework license, or call the main model on the desk Mac. There is no p50 or
p95, and no side-by-side latency against the person's main model, for these
three lines or for autocomplete, rewrite, title, classification, extraction,
or summary. Those comparisons belong on the desk Mac after the helper is built
there.

The headless check on Linux uses a fixture with the same helper protocol. It
proves routing, schema-miss fallback, and the ledger row. It does not measure
the model.
