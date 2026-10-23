// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type PromotionPolicy struct {
	// A dated, nonrecurring statement older than this UTC timestamp is excluded.
	Horizon int64
}
type PromotionBatch struct {
	ID, Agent, Person, Quote string
	OccurredAt               int64
	Sources                  []string
	Precision                memory.Precision
	PersonaProposal          bool
}

func instruction(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"ignore previous", "ignore all", "you must ", "always obey", "system:", "developer:", "answer ", "respond ", "이전 지시", "항상 답", "무조건 "} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// PromotionBatches proposes verbatim quotes, never facts extracted from summary
// text. Acceptance attests that a cluster concerns the person; it is not an
// inferred provenance upgrade. The source language and precision stay intact.
func (p *Plan) PromotionBatches(policy PromotionPolicy) ([]PromotionBatch, error) {
	groups := map[string]*PromotionBatch{}
	superseded := map[string]bool{}
	for _, c := range p.conversations {
		for _, r := range c.records {
			for _, l := range r.links {
				if l.kind == "supersedes" {
					superseded[l.target] = true
				}
			}
		}
	}
	for _, c := range p.conversations {
		for _, r := range c.records {
			if superseded[r.id] || hasFlag(r, "retracted") || strings.TrimSpace(r.body) == "" || len(ScanSecrets([]byte(r.body))) > 0 {
				continue
			}
			// Unavailable bodies and secrets recognized only after JSON decoding
			// cannot become verbatim promotion drafts or memory evidence.
			masked, err := secrets.Mask(r.body)
			if err != nil || len(masked.Spans()) != 0 {
				continue
			}
			person := p.people[c.report.Profile]
			precision := memory.Session
			var occurred int64
			if r.event != nil {
				e := r.event
				if e.classified.kind != guard.Person || e.classified.unproven || e.replay != "" {
					continue
				}
				view := false
				for _, l := range r.links {
					view = view || l.kind == "view_of"
				}
				if view {
					continue
				}
				person = e.classified.person
				precision = memory.Exact
				at, err := time.Parse(time.RFC3339Nano, e.occurred)
				if err != nil {
					return nil, err
				}
				occurred = at.Unix()
			} else if r.kind == "card" || r.kind == "conclusion" {
				// Registered history metadata can rule out non-person rows. Unknown
				// subjects remain review drafts and require explicit person attestation.
				nonPerson := false
				for _, s := range r.sources {
					subject := s.row.text("subject_kind")
					nonPerson = nonPerson || (subject != "" && subject != "person")
					if t := s.row.number("timestamp"); t > 0 {
						occurred = max(occurred, int64(t))
					}
				}
				if nonPerson {
					continue
				}
			} else {
				continue
			}
			if person == "" {
				return nil, ErrSnapshot
			}
			key := opaque(r.agent, person, strings.Join(strings.Fields(strings.ToLower(r.body)), " "), string(precision))
			g := groups[key]
			if g == nil {
				g = &PromotionBatch{Agent: r.agent, Person: person, Quote: r.body, Precision: precision, PersonaProposal: instruction(r.body)}
				groups[key] = g
			}
			if occurred > g.OccurredAt {
				g.Quote = r.body
				g.OccurredAt = occurred
			}
			g.Sources = append(g.Sources, r.id)
		}
	}
	var result []PromotionBatch
	for _, g := range groups {
		if policy.Horizon > 0 && g.OccurredAt > 0 && g.OccurredAt < policy.Horizon && len(g.Sources) < 2 {
			continue
		}
		sort.Strings(g.Sources)
		g.ID = opaque(g.Agent, g.Person, g.Quote, string(g.Precision), strings.Join(g.Sources, ","))
		result = append(result, *g)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt != result[j].OccurredAt {
			return result[i].OccurredAt > result[j].OccurredAt
		}
		return result[i].ID < result[j].ID
	})
	return result, nil
}

// Promote accepts exact reviewed cluster identities. All items and receipts
// commit together, and receipts survive item retraction/deletion so reimport
// never resurrects a forgotten item. The host supplies a person confirmation,
// not a worker echo or text found in source data.
func (p *Plan) Promote(ctx context.Context, db Canon, policy PromotionPolicy, accepted []string, confirmation string) (int, error) {
	if strings.TrimSpace(confirmation) == "" {
		return 0, ErrSnapshot
	}
	batches, err := p.PromotionBatches(policy)
	if err != nil {
		return 0, err
	}
	byID := map[string]PromotionBatch{}
	records := map[string]*record{}
	for _, b := range batches {
		byID[b.ID] = b
	}
	for _, c := range p.conversations {
		for _, r := range c.records {
			records[r.id] = r
		}
	}
	seen := map[string]bool{}
	for _, id := range accepted {
		b, ok := byID[id]
		if !ok || b.PersonaProposal || seen[id] {
			return 0, ErrSnapshot
		}
		seen[id] = true
	}
	added := 0
	err = db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := p.verifyTx(ctx, tx); err != nil {
			return err
		}
		for _, id := range accepted {
			b := byID[id]
			for _, rid := range b.Sources {
				var generation string
				if err := tx.QueryRowContext(ctx, "SELECT backup_generation FROM import_seal WHERE conversation_id=?", records[rid].conversation).Scan(&generation); err != nil {
					return err
				}
				if generation == "" {
					return ErrSnapshot
				}
			}
			var previous string
			err := tx.QueryRowContext(ctx, "SELECT person_id FROM import_promotion WHERE cluster_id=?", id).Scan(&previous)
			if err == nil {
				if previous != b.Person {
					return ErrConflict
				}
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var evidence []memory.Evidence
			for _, rid := range b.Sources {
				kind := memory.External
				if records[rid].event != nil {
					kind = memory.Event
				}
				evidence = append(evidence, memory.Evidence{Kind: kind, ID: rid, Precision: b.Precision})
			}
			item, err := memory.Promote(ctx, tx, memory.Write{Agent: b.Agent, Person: b.Person, Item: memory.Item{Kind: memory.Fact, Content: b.Quote, Subject: memory.Subject{Kind: memory.Person, ID: b.Person}, Basis: memory.Imported, Evidence: evidence}}, confirmation,
				func(ctx context.Context, tx *canon.Tx, ref memory.Evidence) (memory.Origin, error) {
					r := records[ref.ID]
					if r == nil {
						return memory.Origin{}, ErrConflict
					}
					origin := memory.Origin{ID: r.id, Person: b.Person, Agent: r.agent, Quote: r.body, OccurredAt: b.OccurredAt, Scope: memory.Local, Verified: true, Speaker: guard.Forwarded}
					if r.event != nil {
						var visible int
						if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_visible_event WHERE id=? AND speaker_kind='person' AND person_id=?", r.id, b.Person).Scan(&visible); err != nil {
							return memory.Origin{}, err
						}
						if visible != 1 {
							return memory.Origin{}, memory.ErrEvidence
						}
						origin.Speaker = r.event.classified.kind
					}
					return origin, nil
				})
			if err != nil {
				return err
			}
			sources, _ := json.Marshal(b.Sources)
			if _, err = tx.ExecContext(ctx, "INSERT INTO import_promotion(cluster_id,confirmation_id,person_id,item_id,confirmed_at,sources) VALUES(?,?,?,?,?,?)", id, confirmation, b.Person, item.ID, time.Now().UTC().Format(time.RFC3339Nano), string(sources)); err != nil {
				return err
			}
			added++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return added, nil
}
