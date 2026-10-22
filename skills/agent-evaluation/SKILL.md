---
name: agent-evaluation
description: Use when comparing workers or models on a bounded task set.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Choose representative tasks and a rubric before interpreting outcomes. Compare
candidates on equivalent inputs, isolation and budgets, and identify the variable
being changed. Evaluate the trajectory separately from the result: a correct
answer can still rely on fragile or inappropriate steps.

Discuss correctness, usefulness, time, cost and verification coverage separately.
Prefer executable evidence for properties it can establish; judge scores are
interpretations, not correctness proofs. Examine how well a judge agrees with
human labels before relying on it. Recommend per scenario with uncertainty and
name the next comparison that could change the recommendation. An anecdote or
an unrun plan cannot support a universal ranking.
