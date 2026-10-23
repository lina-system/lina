// SPDX-License-Identifier: Apache-2.0
// Package planning validates Klotho proposals before the worker sends them to
// Core. It owns no records, credentials, execution or confirmation authority.
package planning

import "github.com/thisisjun786/lina/internal/moirai/jobapi"

func Validate(kind string, draft jobapi.PlanDraft) error { return jobapi.ValidatePlan(kind, draft) }
