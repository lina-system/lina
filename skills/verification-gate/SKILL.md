---
name: verification-gate
description: Use when a delivery claim needs evidence against its work definition.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Map each acceptance requirement to the observation that would actually prove it.
Judge whether the available checks cover the changed behavior, including failure
paths and generated outputs; a narrow test may leave a wider claim open.

Explain what the evidence establishes and which requirement is still uncertain.
Separate reproduction, implementation, verification, delivery and deployment.
If a failed check blocks that assessment, identify the smallest investigation
that would resolve the gap. Core supplies check status and receipt validity;
this method supplies the judgment about coverage, not a merge verdict.
