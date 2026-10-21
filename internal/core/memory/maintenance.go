// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

const (
	ClearStanding    = "clear_standing"
	MergeDuplicates  = "merge_duplicates"
	MarkAutomation   = "mark_automation"
	ReviewDuplicate  = "review_duplicate"
	ReviewExpired    = "review_expired"
	ReviewSuperseded = "review_superseded"
	ProposalLifetime = int64(7 * 24 * 60 * 60)
)

type MaintenanceChoice struct {
	Person   string   `json:"person"`
	Holder   Holder   `json:"holder"`
	Revision int64    `json:"revision"`
	Allowed  []string `json:"allowed"`
}

// SetMaintenanceChoice is for a person-authorized settings flow, never a model
// proposal. Revision is a compare-and-swap on the editable, inspectable choice.
func SetMaintenanceChoice(ctx context.Context, tx *canon.Tx, choice MaintenanceChoice) error {
	if tx == nil || !present(choice.Person) || !present(string(choice.Holder)) || choice.Revision < 1 {
		return ErrInvalid
	}
	for _, a := range choice.Allowed {
		if a != ClearStanding && a != MergeDuplicates && a != MarkAutomation {
			return ErrInvalid
		}
	}
	current, err := maintenanceChoice(ctx, tx, choice.Person, choice.Holder)
	if err != nil {
		return err
	}
	if choice.Revision != current.Revision+1 {
		return ErrJobConflict
	}
	raw, err := json.Marshal(choice)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_maintenance_choice VALUES(?,?,?,?) ON CONFLICT(person,holder) DO UPDATE SET revision=excluded.revision,record=excluded.record`, choice.Person, choice.Holder, choice.Revision, raw)
	return err
}
func maintenanceChoice(ctx context.Context, tx *canon.Tx, person string, holder Holder) (MaintenanceChoice, error) {
	choice := MaintenanceChoice{Person: person, Holder: holder}
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT record FROM memory_maintenance_choice WHERE person=? AND holder=?", person, holder).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return choice, nil
	}
	if err != nil {
		return choice, err
	}
	err = json.Unmarshal(raw, &choice)
	return choice, err
}
func (s *Store) MaintenanceChoice(ctx context.Context, person string, holder Holder) (MaintenanceChoice, error) {
	var choice MaintenanceChoice
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		choice, err = maintenanceChoice(ctx, tx, person, holder)
		return err
	})
	return choice, err
}

type MaintenanceProposal struct {
	ID            string `json:"id"`
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	Item          string `json:"item"`
	Revision      int64  `json:"revision"`
	Other         string `json:"other,omitempty"`
	OtherRevision int64  `json:"other_revision,omitempty"`
	Expires       int64  `json:"expires"`
}
type MaintenanceResult struct {
	Applied, Proposed int
	Notice            string
}

func (s *Store) MaintenanceProposals(ctx context.Context, person string, holder Holder, now int64) ([]MaintenanceProposal, error) {
	var result []MaintenanceProposal
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT p.record FROM memory_maintenance_proposal p
   JOIN retract_visible_memory m ON m.id=json_extract(p.record,'$.item')
   AND json_extract(m.record,'$.revision')=json_extract(p.record,'$.revision') AND m.state='active'
   WHERE p.person=? AND p.holder=? AND p.expires>?
   AND (coalesce(json_extract(p.record,'$.other'),'')='' OR EXISTS(
    SELECT 1 FROM retract_visible_memory n WHERE n.id=json_extract(p.record,'$.other')
    AND n.state='active' AND json_extract(n.record,'$.revision')=json_extract(p.record,'$.other_revision')))
   ORDER BY p.id`, person, holder, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var p MaintenanceProposal
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			result = append(result, p)
		}
		return rows.Err()
	})
	return result, err
}

// Maintain runs exactly one pass over one holder. All detection and authority
// checks happen inside Apply, so neither a stale task nor model output can
// authorize changes. Expired proposals remain receipts and are never renewed
// just because the next pass sees the same unchanged item.
func Maintain(ctx context.Context, tx *canon.Tx, person string, holder Holder, now int64) (MaintenanceResult, error) {
	result := MaintenanceResult{}
	if tx == nil || !present(person) || !present(string(holder)) || now < 0 {
		return result, ErrInvalid
	}
	choice, err := maintenanceChoice(ctx, tx, person, holder)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.record,EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=m.id AND n.valid_from<=?) FROM retract_visible_memory m WHERE m.person=? AND m.holder=? AND m.state='active' ORDER BY m.sequence DESC`, now, person, holder)
	if err != nil {
		return result, err
	}
	var items []Item
	superseded := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var item Item
		var older bool
		if err := rows.Scan(&raw, &older); err != nil {
			rows.Close()
			return result, err
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			rows.Close()
			return result, err
		}
		items = append(items, item)
		superseded[item.ID] = older
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return result, err
	}
	var proposals []MaintenanceProposal
	for _, item := range items {
		reason := ""
		if item.ValidUntil != 0 && item.ValidUntil <= now {
			reason = "expired"
		} else if superseded[item.ID] {
			reason = "superseded"
		} else if instructionLike(item.Content) {
			reason = "instruction_like"
		}
		if item.Standing && reason != "" {
			proposals = append(proposals, MaintenanceProposal{Action: ClearStanding, Reason: reason, Item: item.ID, Revision: item.Revision})
		} else if reason == "expired" || reason == "superseded" {
			action := ReviewExpired
			if reason == "superseded" {
				action = ReviewSuperseded
			}
			proposals = append(proposals, MaintenanceProposal{Action: action, Reason: reason, Item: item.ID, Revision: item.Revision})
		}
		if item.Basis != Automation && len(item.Evidence) > 0 {
			automated := true
			for _, e := range item.Evidence {
				var speaker string
				if e.Kind != Event {
					automated = false
					break
				}
				if err := tx.QueryRowContext(ctx, "SELECT speaker_kind FROM conv_event WHERE id=?", e.ID).Scan(&speaker); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						automated = false
						break
					}
					return result, err
				}
				if speaker != "automation" {
					automated = false
					break
				}
			}
			if automated {
				proposals = append(proposals, MaintenanceProposal{Action: MarkAutomation, Reason: "automation_only", Item: item.ID, Revision: item.Revision})
			}
		}
	}
	// Pick one survivor per exact cluster. Similar wording is never merged merely
	// on token overlap, because a negation can reverse a statement's meaning.
	losers := map[string]bool{}
	for i, a := range items {
		if losers[a.ID] {
			continue
		}
		for _, b := range items[i+1:] {
			if losers[b.ID] || a.Subject != b.Subject || a.Scope != b.Scope || a.Kind != b.Kind || a.ValidFrom != b.ValidFrom || a.ValidUntil != b.ValidUntil || a.Supersedes != "" || b.Supersedes != "" || superseded[a.ID] || superseded[b.ID] {
				continue
			}
			action := ""
			if normalize(a.Content) == normalize(b.Content) {
				action = MergeDuplicates
				losers[b.ID] = true
			} else if tokenOverlap(a.Content, b.Content) >= 0.8 {
				action = ReviewDuplicate
			}
			if action != "" {
				proposals = append(proposals, MaintenanceProposal{Action: action, Reason: "same_subject", Item: a.ID, Revision: a.Revision, Other: b.ID, OtherRevision: b.Revision})
			}
		}
	}
	for _, p := range proposals {
		// The receipt identity excludes expiry, so an unanswered proposal expires.
		key, _ := json.Marshal([]any{person, holder, p.Action, p.Reason, p.Item, p.Revision, p.Other, p.OtherRevision})
		sum := sha256.Sum256(key)
		p.ID = hex.EncodeToString(sum[:])
		p.Expires = now + ProposalLifetime
		if slices.Contains(choice.Allowed, p.Action) {
			if err := applyMaintenance(ctx, tx, p); err != nil {
				return MaintenanceResult{}, err
			}
			result.Applied++
			if _, err := tx.ExecContext(ctx, "DELETE FROM memory_maintenance_proposal WHERE id=?", p.ID); err != nil {
				return MaintenanceResult{}, err
			}
		} else {
			raw, _ := json.Marshal(p)
			r, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO memory_maintenance_proposal VALUES(?,?,?,?,?)", p.ID, person, holder, p.Expires, raw)
			if err != nil {
				return MaintenanceResult{}, err
			}
			n, err := r.RowsAffected()
			if err != nil {
				return MaintenanceResult{}, err
			}
			result.Proposed += int(n)
		}
	}
	if result.Applied+result.Proposed > 0 {
		result.Notice = fmt.Sprintf("Memory maintenance: %d changes applied; %d proposals ready.", result.Applied, result.Proposed)
	}
	return result, nil
}
func applyMaintenance(ctx context.Context, tx *canon.Tx, p MaintenanceProposal) error {
	item, err := readItem(ctx, tx, p.Item)
	if err != nil {
		return err
	}
	switch p.Action {
	case ClearStanding:
		item.Standing = false
	case MarkAutomation:
		if item.Subject.Kind == Person {
			return ErrPersonEvidence
		}
		item.Basis = Automation
	case MergeDuplicates:
		other, err := readItem(ctx, tx, p.Other)
		if err != nil {
			return err
		}
		for _, e := range other.Evidence {
			if !slices.ContainsFunc(item.Evidence, func(v Evidence) bool { return v.Kind == e.Kind && v.ID == e.ID && v.Revision == e.Revision }) {
				item.Evidence = append(item.Evidence, e)
			}
		}
		for _, tag := range other.Sensitivity {
			if !slices.Contains(item.Sensitivity, tag) {
				item.Sensitivity = append(item.Sensitivity, tag)
			}
		}
		if other.Basis == Stated {
			item.Basis = Stated
			item.SourcePerson = other.SourcePerson
		}
		item.Standing = item.Standing || other.Standing
		other.State = Retracted
		other.Standing = false
		if err := saveJobRevision(ctx, tx, other); err != nil {
			return err
		}
	default:
		return ErrInvalid
	}
	return saveJobRevision(ctx, tx, item)
}
func saveJobRevision(ctx context.Context, tx *canon.Tx, item Item) error {
	item.Revision++
	slices.Sort(item.Sensitivity)
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE memory_item SET record=?,state=?,standing=? WHERE id=?", raw, item.State, item.Standing, item.ID); err != nil {
		return err
	}
	return revision(ctx, tx, item, raw)
}
func tokenOverlap(a, b string) float64 {
	left := strings.Fields(normalize(a))
	right := strings.Fields(normalize(b))
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, v := range left {
		set[v] = true
	}
	other := map[string]bool{}
	for _, v := range right {
		other[v] = true
	}
	n := 0
	for v := range other {
		if set[v] {
			n++
		}
	}
	return float64(n) / float64(len(set)+len(other)-n)
}
func maskedJobText(text string) (string, error) {
	masked, err := secrets.Mask(text)
	if err != nil {
		return "", err
	}
	return masked.Text(), nil
}

// MaintenanceApply binds a deterministic job to a holder. Its task is only a
// trigger; policy and item state are read afresh in the transaction. The result
// callback may write a notice in that transaction; it must not perform I/O or
// announce success before commit. A no-op never calls it.
func MaintenanceApply(person string, holder Holder, now func() int64, changed func(context.Context, *canon.Tx, MaintenanceResult) error) func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error {
	return func(ctx context.Context, tx *canon.Tx, _ jobapi.Task, _ json.RawMessage) error {
		if now == nil {
			return ErrInvalid
		}
		result, err := Maintain(ctx, tx, person, holder, now())
		if err != nil {
			return err
		}
		if result.Notice != "" && changed != nil {
			return changed(ctx, tx, result)
		}
		return nil
	}
}
