// SPDX-License-Identifier: Apache-2.0
package persona

import (
	"encoding/json"
	"fmt"
	"maps"
)

// Release expectations transcribe docs/design/preset-work-methods.md: The record,
// Done evidence, Hand-offs, and Starter records and persona changes. Keep these
// independent of the shipped JSON and methodFields: either can drift while still
// passing schema validation. Runtime publication and agent fills use the schema,
// not these release-only expectations.
var starterMethodDefaults = map[string]string{
	"tools.declare_first": `[]`,
	"workers.dispatch":    `false`,
	"background_runs":     `["research","reading","drafting","checks"]`,
	"reply.status":        `"verdict-first"`,
	"reply.comparison":    `"verdict-then-card"`,
	"reply.candidates":    `1`,
	"reply.third_party":   `"recipient-only"`,
	"ask.per_turn":        `1`,
	"ask.form":            `"narrowed"`,
	"ask.unanswered":      `"default"`,
	"handoff.owns":        `[]`,
	"handoff.check_back":  `true`,
	"done.service":        `["read_back_running"]`,
	"done.interface":      `["real_view"]`,
	"done.data":           `["live_vs_shown"]`,
	"done.message":        `["sent_copy"]`,
	"done.code":           `["owner_path_run"]`,
	"done.document":       `["source_reopened"]`,
	"done.purchase":       `["receipt"]`,
	"done.peer_report":    `["labelled_claim"]`,
	"fill":                `[]`,
}

// starterMethodValues holds the per-starter overrides the release ships. It is
// empty: the M4-4 replay measured the M4-3 tailoring and it did not ship, so
// every starter ships the empty record and its defaults until M4-2 revises it.
var starterMethodValues = map[string]map[string]string{}

func validateStarterMethod(key, record string) error {
	overrides, ok := starterMethodValues[key]
	if !ok {
		overrides = map[string]string{}
	}
	want := maps.Clone(starterMethodDefaults)
	maps.Copy(want, overrides)
	values := WorkMethodValues(record)
	fill := WorkMethodFillKeys(record)
	if fill == nil {
		fill = []string{}
	}
	values["fill"], _ = json.Marshal(fill)
	if len(values) != len(want) {
		return ErrWorkMethod
	}
	for field, expected := range want {
		// ParseWorkMethod has already canonicalized the JSON. Lists retain their
		// order, including the declared-first tools; absent fields use defaults.
		if string(values[field]) != expected {
			return fmt.Errorf("%s: %w", field, ErrWorkMethod)
		}
	}
	return nil
}
