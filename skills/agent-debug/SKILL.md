---
name: agent-debug
description: Use when a worker stalls, repeats errors or loses the task intent.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Read the bounded execution evidence around the failure: the task intent,
expected behavior, tool sequence and context changes. Separate a tool or service
failure from a mistaken hypothesis, instruction drift or context pressure.
Compare rival explanations and choose the cheapest observation that separates
them.

Look for repeated assumptions across unsuccessful attempts and for a change
between the last working run and the failing one. Suggest the smallest
reversible intervention with a clear observation that would test it. Report
what the evidence supports and what remains unknown. Core owns execution
identity, live state and recovery; this diagnosis does not restart a worker or
turn an observation gap into a terminal result.
