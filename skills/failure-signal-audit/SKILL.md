---
name: failure-signal-audit
description: Use when errors may be hidden by fallbacks or unsupported success claims.
---
<!-- SPDX-License-Identifier: Apache-2.0 -->

Trace a suspected failure from its source through callers to the reported
result. Compare an intended fallback with a masked failure before blaming a
handler. Look for lost context, ignored rejections and empty defaults that make
unavailable data look valid.

Use the cheapest discriminating observation to establish the user impact. When
possible, compare behavior with and without the suspected masking change in an
isolated reproduction. Distinguish swallowed errors, harmful fallbacks,
propagation gaps and unsupported success claims. Report the location, evidence,
impact and smallest remediation for each finding. Core decides mechanical check
status; this audit judges whether the checks expose the relevant failures.
