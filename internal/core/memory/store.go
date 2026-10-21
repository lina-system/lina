// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type Store struct{ db *canon.Store }

func New(db *canon.Store) *Store { return &Store{db: db} }
func normalize(s string) string  { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
func present(s string) bool      { return strings.TrimSpace(s) != "" }
func validScope(s Scope) bool    { return s == Local || s == Private || s == NeverLeaves }
func scopeRank(s Scope) int {
	if s == NeverLeaves {
		return 2
	}
	if s == Private {
		return 1
	}
	return 0
}
func validKind(k Kind) bool {
	switch k {
	case Fact, Preference, Commitment, Decision, Correction, SharedHistory, Relationship, EmotionObservation:
		return true
	}
	return false
}
func validBasis(b Basis) bool {
	switch b {
	case Stated, Derived, Imported, Automation:
		return true
	}
	return false
}
func defaultHolder(in Write) Holder {
	if in.Item.Subject.Kind == ThirdParty || in.AgentPreference {
		return Holder(in.Agent)
	}
	switch in.Item.Kind {
	case SharedHistory, Relationship, EmotionObservation:
		return Holder(in.Agent)
	case Fact, Preference, Commitment, Decision, Correction:
		return All
	}
	return ""
}
func validate(in Write) (Item, error) {
	item := in.Item
	for _, text := range []string{item.Content, item.Gloss} {
		masked, err := secrets.Mask(text)
		if err != nil || masked.Text() != text {
			return Item{}, ErrInvalid
		}
	}
	if !present(in.Agent) || in.Agent == string(All) || !present(in.Person) || !validKind(item.Kind) || !validBasis(item.Basis) || !present(item.Content) || item.ID != "" || item.Revision != 0 || item.State != "" || len(item.Evidence) == 0 || item.ValidFrom < 0 || item.ValidUntil < 0 || item.ValidUntil != 0 && item.ValidUntil <= item.ValidFrom {
		return Item{}, ErrInvalid
	}
	if in.AgentPreference && item.Kind != Preference {
		return Item{}, ErrInvalid
	}
	// Correction, promotion and holder moves need their own authenticated flows.
	if item.Kind == Correction || item.Basis == Imported {
		return Item{}, ErrInvalid
	}
	switch item.Subject.Kind {
	case Person:
		if item.Subject.ID != in.Person {
			return Item{}, ErrInvalid
		}
	case ThirdParty:
		if !present(item.Subject.ID) {
			return Item{}, ErrInvalid
		}
	case World:
	default:
		return Item{}, ErrInvalid
	}
	holder := defaultHolder(in)
	if item.Holder != "" && item.Holder != holder && !in.ExplicitHolder {
		return Item{}, ErrScope
	}
	if item.Holder == "" {
		item.Holder = holder
	}
	if item.Holder != All && item.Holder != Holder(in.Agent) {
		return Item{}, ErrScope
	}
	if item.Scope == "" {
		item.Scope = Local
		if item.Subject.Kind == ThirdParty {
			item.Scope = Private
		}
	}
	if !validScope(item.Scope) {
		return Item{}, ErrScope
	}
	if item.Person != "" && item.Person != in.Person {
		return Item{}, ErrInvalid
	}
	item.Person = in.Person
	item.SourceAgent = in.Agent
	item.SourcePerson = ""
	item.State = Active
	item.Revision = 1
	item.Sensitivity = nil
	if item.Subject.Kind == ThirdParty {
		item.Sensitivity = []string{"third_party"}
	}
	if in.AgentPreference {
		item.Standing = false
	}
	if item.Standing && (item.Subject.Kind != Person || item.Kind != Fact && item.Kind != Preference || item.ValidFrom != 0 || item.ValidUntil != 0) {
		return Item{}, ErrInvalid
	}
	return item, nil
}

// Insert is a transaction operation, not a success receipt. Only the caller of
// Store.Write may acknowledge remembering, after that enclosing commit returns.
func Insert(ctx context.Context, tx *canon.Tx, in Write, resolve ResolveEvidence) (Item, error) {
	item, err := validate(in)
	if err != nil {
		return Item{}, err
	}
	if tx == nil || resolve == nil {
		return Item{}, ErrEvidence
	}
	evidence := make([]Evidence, 0, len(item.Evidence))
	allAutomation := true
	for _, e := range item.Evidence {
		if !present(e.ID) || e.Revision < 0 || (e.Precision != Exact && e.Precision != Session && e.Precision != NoPrecision) {
			return Item{}, ErrEvidence
		}
		switch e.Kind {
		case Event, Material, WorkResult, MemoryItem, External:
		default:
			return Item{}, ErrEvidence
		}
		if item.Subject.Kind == Person && (e.Kind != Event || e.Precision != Exact) {
			return Item{}, ErrPersonEvidence
		}
		origin, err := resolve(ctx, tx, e)
		if err != nil {
			return Item{}, err
		}
		if item.Subject.Kind == Person && origin.Speaker != guard.Person {
			return Item{}, ErrPersonEvidence
		}
		if origin.Speaker == guard.ExternalParticipant || origin.Person != in.Person || origin.Agent != "" && origin.Agent != in.Agent || origin.PlanEnded {
			return Item{}, ErrEvidence
		}
		if in.Item.Scope == "" && scopeRank(origin.Scope) > scopeRank(item.Scope) {
			item.Scope = origin.Scope
		}
		if !validScope(origin.Scope) || scopeRank(item.Scope) < scopeRank(origin.Scope) {
			return Item{}, ErrScope
		}
		if e.Kind == WorkResult && !origin.Verified || e.Kind == External && item.Kind == Fact {
			return Item{}, ErrEvidence
		}
		if item.Basis == Stated && (e.Kind != Event || origin.Speaker != guard.Person) {
			return Item{}, ErrEvidence
		}
		if e.Kind != Event || origin.Speaker != guard.Automation {
			allAutomation = false
		}
		if origin.Speaker == guard.Person {
			item.SourcePerson = in.Person
		}
		if e.Kind == Event && origin.ID != "" {
			e.ID = origin.ID
		}
		e.SourceAgent = origin.Agent
		e.OccurredAt = origin.OccurredAt
		e.Quote = origin.Quote
		if !slices.ContainsFunc(evidence, func(old Evidence) bool { return old.Kind == e.Kind && old.ID == e.ID && old.Revision == e.Revision }) {
			evidence = append(evidence, e)
		}
		for _, tag := range origin.Sensitivity {
			if !slices.Contains(item.Sensitivity, tag) {
				item.Sensitivity = append(item.Sensitivity, tag)
			}
		}
	}
	if allAutomation {
		item.Basis = Automation
	} else if item.Basis == Automation {
		return Item{}, ErrEvidence
	}
	item.Evidence = evidence
	slices.Sort(item.Sensitivity)
	if item.Supersedes != "" {
		previous, err := readItem(ctx, tx, item.Supersedes)
		if err != nil || previous.Person != item.Person || previous.Subject != item.Subject || previous.Holder != item.Holder || previous.Scope != item.Scope || previous.State != Active || item.Basis != Stated && previous.Basis == Stated {
			return Item{}, ErrSupersedes
		}
		var successors int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM memory_item WHERE supersedes=?", item.Supersedes).Scan(&successors); err != nil {
			return Item{}, err
		}
		if successors != 0 {
			return Item{}, ErrSupersedes
		}
	}
	// Equality is inside one subject and scope; it never joins different people,
	// private holders, or statements whose validity intervals differ.
	rows, err := tx.QueryContext(ctx, `SELECT record FROM memory_item WHERE person=? AND subject_kind=? AND subject_id=? AND holder=? AND normalized=? AND scope=? AND state='active' AND valid_from=? AND valid_until=? AND supersedes=? AND NOT EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=memory_item.id)`, item.Person, item.Subject.Kind, item.Subject.ID, item.Holder, normalize(item.Content), item.Scope, item.ValidFrom, item.ValidUntil, item.Supersedes)
	if err != nil {
		return Item{}, err
	}
	var duplicate *Item
	for rows.Next() {
		var raw []byte
		var old Item
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &old)
		}
		if err != nil {
			break
		}
		if old.Kind == item.Kind {
			duplicate = &old
			break
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return Item{}, err
	}
	if duplicate != nil {
		old := *duplicate
		changed := false
		for _, e := range item.Evidence {
			if !slices.ContainsFunc(old.Evidence, func(v Evidence) bool { return v.Kind == e.Kind && v.ID == e.ID && v.Revision == e.Revision }) {
				old.Evidence = append(old.Evidence, e)
				changed = true
			}
		}
		for _, tag := range item.Sensitivity {
			if !slices.Contains(old.Sensitivity, tag) {
				old.Sensitivity = append(old.Sensitivity, tag)
				changed = true
			}
		}
		if old.Basis != Stated && item.Basis == Stated {
			old.Basis = Stated
			old.SourcePerson = item.SourcePerson
			changed = true
		}
		if !changed {
			return old, nil
		}
		old.Revision++
		slices.Sort(old.Sensitivity)
		raw, err := json.Marshal(old)
		if err != nil {
			return Item{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE memory_item SET record=? WHERE id=?", raw, old.ID); err != nil {
			return Item{}, err
		}
		if err = revision(ctx, tx, old, raw); err != nil {
			return Item{}, err
		}
		return old, nil
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	item.ID = hex.EncodeToString(id[:])
	raw, err := json.Marshal(item)
	if err != nil {
		return Item{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Person, item.Holder, item.Subject.Kind, item.Subject.ID, normalize(item.Content), item.Content, item.Scope, item.State, item.ValidFrom, item.ValidUntil, item.Supersedes, item.Standing, raw)
	if err != nil {
		return Item{}, err
	}
	if err = revision(ctx, tx, item, raw); err != nil {
		return Item{}, err
	}
	return item, nil
}
func revision(ctx context.Context, tx *canon.Tx, item Item, raw []byte) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO memory_revision(item_id,revision,record) VALUES(?,?,?)", item.ID, item.Revision, raw)
	return err
}
func readItem(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	var raw []byte
	var item Item
	if err := tx.QueryRowContext(ctx, "SELECT record FROM memory_item WHERE id=?", id).Scan(&raw); err != nil {
		return item, err
	}
	err := json.Unmarshal(raw, &item)
	return item, err
}

// Recall returns current, scoped records. Text matching is deliberately a
// literal substring scan until the indexed Korean search in #65 is installed.
func (s *Store) Recall(ctx context.Context, scope RecallScope, q Query) ([]Hit, error) {
	return s.recall(ctx, scope, q, false)
}
func (s *Store) recall(ctx context.Context, scope RecallScope, q Query, standing bool) ([]Hit, error) {
	if s == nil || s.db == nil || !present(scope.Agent) || scope.Agent == string(All) || !present(scope.Person) || q.At < 0 || q.Limit < 0 {
		return nil, ErrInvalid
	}
	limit := q.Limit
	if limit == 0 {
		limit = 100
	}
	var hits []Hit
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		holders := scope.Holders()
		restricted, err := restrictedEvents(ctx, tx, scope)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT m.record,
   ((m.valid_until<>0 AND m.valid_until<=?) OR EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=m.id AND n.valid_from<=?)) AS older
   FROM memory_item m WHERE m.person=? AND m.holder IN (?,?) AND m.state='active' AND m.valid_from<=?
   AND (?=0 OR m.scope='local') AND (?=0 OR m.scope<>'never-leaves') AND (?=0 OR m.standing=1)
   AND (?=0 OR m.subject_kind='person')
   ORDER BY (SELECT max(r.sequence) FROM memory_revision r WHERE r.item_id=m.id) DESC,m.id`, q.At, q.At, scope.Person, holders[0], holders[1], q.At, scope.Projection, scope.ExternalModel, standing, q.PersonOnly)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var hit Hit
			if err := rows.Scan(&raw, &hit.Older); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &hit.Item); err != nil {
				return err
			}
			blocked := false
			for _, e := range hit.Evidence {
				if e.Kind == Event && restricted[e.ID] {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
			if hit.Older && !q.History || !strings.Contains(normalize(hit.Content), normalize(q.Text)) {
				continue
			}
			for i := range hit.Evidence {
				if hit.Evidence[i].SourceAgent != "" && hit.Evidence[i].SourceAgent != scope.Agent {
					hit.Evidence[i].Quote = ""
				}
			}
			hits = append(hits, hit)
			if len(hits) == limit {
				break
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// Profile is a bounded, ephemeral view, never a second store. Counter is the
// same deterministic tokenizer the request assembler uses.
func (s *Store) Profile(ctx context.Context, scope RecallScope, at int64, lines, tokens int, count func(string) int) ([]Hit, error) {
	if lines < 0 || tokens < 0 || count == nil {
		return nil, ErrInvalid
	}
	if lines == 0 || tokens == 0 {
		return nil, nil
	}
	hits, err := s.recall(ctx, scope, Query{At: at, Limit: lines, PersonOnly: true}, true)
	if err != nil {
		return nil, err
	}
	var result []Hit
	for _, hit := range hits {
		text := strings.Join(strings.Fields(hit.Content), " ")
		n := count(text)
		if n < 0 {
			return nil, ErrInvalid
		}
		if n > tokens {
			break
		}
		hit.Content = text
		result = append(result, hit)
		tokens -= n
	}
	return result, nil
}

// RestrictedEvents prevents raw ledger fallback or a second item from bypassing
// the named scope of memory resting on the same event. It exposes ids only.
func (s *Store) RestrictedEvents(ctx context.Context, scope RecallScope) (map[string]bool, error) {
	if s == nil || s.db == nil || !present(scope.Agent) || scope.Agent == string(All) || !present(scope.Person) {
		return nil, ErrInvalid
	}
	var ids map[string]bool
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; ids, err = restrictedEvents(ctx, tx, scope); return err })
	return ids, err
}
func restrictedEvents(ctx context.Context, tx *canon.Tx, scope RecallScope) (map[string]bool, error) {
	ids := map[string]bool{}
	if !scope.Projection && !scope.ExternalModel {
		return ids, nil
	}
	holders := scope.Holders()
	rows, err := tx.QueryContext(ctx, `SELECT record FROM memory_item WHERE person=? AND holder IN (?,?)
  AND ((?=1 AND scope IN ('private','never-leaves')) OR (?=1 AND scope='never-leaves'))`, scope.Person, holders[0], holders[1], scope.Projection, scope.ExternalModel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var item Item
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		for _, e := range item.Evidence {
			if e.Kind == Event {
				ids[e.ID] = true
			}
		}
	}
	return ids, rows.Err()
}
