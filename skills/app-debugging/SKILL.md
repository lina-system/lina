---
name: app-debugging
description: Use for incorrect application behavior whose cause is unknown.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Separate the observed symptom from the expected behavior. Build a reproduction
that distinguishes the fault from environmental noise. Consider rival causes
across input, state, ordering and dependencies; describe what would refute each.

Choose the cheapest observation that separates the surviving explanations.
For intermittent faults, examine ordering and shared state rather than adding
sleeps or retries. Turn the demonstrated failure into a regression and make the
smallest change that explains it. Compare the same reproduction before and after.
If the fault disappears under instrumentation, keep the timing hypothesis open.
Report the cause, competing explanations and remaining uncertainty.
