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

// starterMethodValues holds the per-starter overrides the release ships: the
// fields that wire the mechanism (declared tool groups, worker dispatch and the
// work kinds each preset owns). The reply-shaping values and the persona-layer
// changes of M4-3 are not shipped: the M4-4 replay measured them, no agent
// improved without another regressing, and the finding returns to M4-2.
var starterMethodValues = map[string]map[string]string{
	"system": {
		"tools.declare_first": `["device","setup","terminal"]`,
		"handoff.owns":        `["setup","device","account","health_of_lina"]`,
	},
	"lina": {
		"tools.declare_first": `["to_do","mail","calendar","messaging_read","documents"]`,
		"handoff.owns":        `["schedule","to_do","mail","business_document","coordination"]`,
	},
	"dev": {
		"tools.declare_first": `["work","github","terminal"]`,
		"workers.dispatch":    `true`,
		"handoff.owns":        `["code","pull_request","worker_task"]`,
	},
	"knowledge": {
		"tools.declare_first": `["people_records","wiki","messaging_read"]`,
		"handoff.owns":        `["people_record","wiki"]`,
	},
	"life": {
		"tools.declare_first": `["life_records","web_research"]`,
		"handoff.owns":        `["life_record","inventory","diet_log"]`,
	},
	"creative": {
		"tools.declare_first": `["creative_artifacts","image_generation","documents"]`,
		"handoff.owns":        `["creative_artifact","creative_tooling_brief"]`,
	},
	"investing": {
		"tools.declare_first": `["web_research","materials"]`,
		"handoff.owns":        `["investment_analysis"]`,
	},
	"legal": {
		"tools.declare_first": `["documents","web_research","materials"]`,
		"handoff.owns":        `["contract_draft","tax_calculation","legal_review"]`,
	},
}

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
