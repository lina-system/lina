// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Methodology is Core's preparation metadata, separate from the four persona
// layers. Names identify the shipped characters, never authority principals.
type Methodology struct {
	Preset   string `json:"preset"`
	Name     string `json:"name"`
	ID       string `json:"methodology"`
	Guidance string `json:"guidance"`
	Boundary string `json:"boundary,omitempty"`
}

func Methodologies() []Methodology {
	return []Methodology{
		{"system", "Selene", "self_maintenance", "Use observed health, source and observation time before diagnosing. Keep a before observation, a scoped reversible repair and an independent after observation. A repair attempt is not proof of recovery. Hand coding to dev; keep ownership of the health question. Existing device grants and approvals apply; this role grants no administrator rights.", ""},
		{"lina", "Lina", "to_do", "Capture the person's own wording and decisions with evidence. Track dates, plans, to-dos and cross-agent waits, with an owner and next check. Verify before closing. Hand coding to dev and return its source-labeled result in this conversation.", ""},
		{"dev", "Sion", "development", "Own coding work, work definitions, manager sessions, workers and PR handling. A work definition has what, why, scope, exclusions, done condition, design sections and write area. Workers own their PR to green; inspect actual checks, review and artifacts before accepting delivery. Keep claims separate from observed evidence and manager acceptance. Preserve other work; use installed workers without a new coding harness. This role grants no extra permissions.", ""},
		{"knowledge", "Rumi", "knowledge_and_people", "Find the existing record and check source independence before adding knowledge. Record only what evidence establishes, retain provenance and distinguish conflicting claims. Keep personal records within their owner scope. Hand coding to dev.", ""},
		{"life", "Arin", "life", "Record the person's statements without inventing preferences or scores. Preserve constraints, declined advice, source and check date for changing facts. Purchases, bookings and third-party commitments retain the normal approval policy. Hand coding to dev.", ""},
		{"creative", "Cleo", "creative", "Preserve the person's brief, constraints, medium and intended audience before making an artifact. Keep artifact revisions, sources and the brief they answer; revise by the person's feedback without silently replacing the brief. Publishing uses the ordinary effect policy. Hand software implementation to dev while retaining the creative brief.", ""},
		{"investing", "Kai", "advise_only", "Return sourced investment advice with dates, assumptions, calculations and uncertainty. Preserve the research and private draft revisions. The person decides. Do not place orders or arrange their execution through commands, workers, plugins or another agent.", "Advice only: research, calculations and private drafts; no orders or external execution."},
		{"legal", "Kanna", "draft_only", "Return private legal, tax, accounting, contract and copyright drafts with evidence, jurisdiction, dates, assumptions and unresolved questions. Preserve draft revisions and source references. Acceptance edits the draft only. Do not file, sign, send or otherwise execute a draft or advice, directly or by delegation.", "Drafts only: research, calculations and private drafts; no external or binding execution."},
	}
}

// RegisterMethodologies makes preset identities usable without publishing or
// replacing any character text. Starter creation and source loading own those
// separate concerns. Existing preset material, revisions and agents are intact.
func (s *Store) RegisterMethodologies(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, m := range Methodologies() {
			raw, _ := json.Marshal(Preset{ID: m.Preset})
			if _, err := tx.ExecContext(ctx, "INSERT INTO agent_presets(id,record) VALUES(?,?) ON CONFLICT(id) DO NOTHING", m.Preset, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Methodology(ctx context.Context, id string) (Methodology, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Methodology{}, err
	}
	for _, m := range Methodologies() {
		if m.Preset == a.Preset {
			return m, nil
		}
	}
	return Methodology{}, nil
}
