---
name: report-pages
description: "Share a registered Desk report link."
version: 1.0.0
author: LINA
license: AGPL-3.0
platforms: [linux, macos]
metadata:
  hermes:
    tags: [desk, reports]
    category: productivity
---

# Report Pages Skill

Use this when a report is too long for chat and is approved for sharing with the recipient.
The Desk Markdown is the source for the report page.

## Prerequisites

Use the configured Desk and its reporting policy. Keep personal material out of shared reports.
The dashboard's public URL must be configured for a link that opens directly from chat.

## Procedure

1. Write or update the Markdown under `1_Goals` or `2_Areas` using the Desk's file guidance.
2. Run `lina desk register '2_Areas/example/weekly-update.md'`, substituting the report's actual path.
   Register the same file again to refresh it immediately after an edit.
3. Send one short summary and the URL the command prints. If the output is only a path, ask the
   operator to configure the dashboard public URL before sharing. If registration refuses a file,
   follow the Desk policy.

Use the [Desk report runbook](../../../docs/desk.md#sharing-a-desk-report) for address setup, the
independent sync timer and rollback to `report-link`.
