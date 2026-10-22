---
name: perf
description: Use when latency, resource use or cost needs investigation.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Define a representative workload and the outcome the person cares about.
Compare baseline measurements under equivalent conditions and explain their
spread. Use profiles or per-stage costs to rank hot paths rather than optimizing
code that merely looks expensive.

Change one supported hypothesis at a time and compare the same workload. Explain
tradeoffs in correctness, latency, throughput, memory and cost separately.
A restart or larger resource allocation can mitigate a symptom without fixing
its cause. For growth over time, investigate the retained objects or resources.
Report the measured bottleneck, the useful improvement and uncertainty; agree
on a regression tolerance appropriate to the workload.
