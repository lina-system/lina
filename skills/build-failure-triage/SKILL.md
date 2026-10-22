---
name: build-failure-triage
description: Use for a group of failing build, test, lint or CI checks.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Read the failing stage and its upstream dependencies before following the last
error. Group failures by likely cause, separating compilation, assertions,
dependencies, configuration and environment. Explain which failures may be
consequences of an earlier one.

Compare the failure with the last known passing context. Distinguish an observed
baseline failure from an inference based on changed lines; leave the origin
unknown when neither resolves it. Propose a patch limited to the demonstrated
cause, with the smallest check that can distinguish a fix from a changed symptom.
Report the clusters, evidence gaps and next investigation. Core's check state
remains authoritative; a triage conclusion does not make a check green.
