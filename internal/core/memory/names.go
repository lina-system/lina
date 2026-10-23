// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"golang.org/x/text/unicode/norm"
)

type NameValue struct {
	Current        string   `json:"current"`
	RetiredAliases []string `json:"retired_aliases,omitempty"`
}

func nameKey(s string) string { return strings.ToLower(norm.NFC.String(s)) }

func validateName(item Item) error {
	if item.Kind != Name {
		if item.Name != nil {
			return ErrInvalid
		}
		return nil
	}
	if item.Name == nil || item.Basis != Stated || item.Subject.ID == "" || item.Supersedes != "" || item.Standing {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, value := range append([]string{item.Name.Current}, item.Name.RetiredAliases...) {
		masked, err := secrets.Mask(value)
		key := nameKey(value)
		if err != nil || masked.Text() != value || strings.TrimSpace(value) != value || value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\t") || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	return nil
}

// putName runs after the ordinary evidence and scope checks, under the fenced
// memory writer. One subject in one holder/scope has one identity across agents.
func putName(ctx context.Context, tx *canon.Tx, item Item) (Item, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT record FROM retract_visible_memory WHERE person=? AND holder=? AND subject_kind=? AND subject_id=? AND scope=? AND state='active' AND json_extract(record,'$.kind')='name' AND valid_from=? AND valid_until=?`, item.Person, item.Holder, item.Subject.Kind, item.Subject.ID, item.Scope, item.ValidFrom, item.ValidUntil).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return insertRecord(ctx, tx, item)
	}
	if err != nil {
		return Item{}, err
	}
	var old Item
	if err := json.Unmarshal(raw, &old); err != nil {
		return Item{}, err
	}
	if old.Name == nil {
		return Item{}, ErrInvalid
	}
	// An older queued turn cannot undo a correction made by a later event.
	var before, after int64
	for _, e := range old.Evidence {
		before = max(before, e.OccurredAt)
	}
	for _, e := range item.Evidence {
		after = max(after, e.OccurredAt)
	}
	if after < before {
		return Item{}, ErrEvidence
	}
	current := nameKey(item.Name.Current)
	aliases := slices.Clone(item.Name.RetiredAliases)
	add := func(alias string) {
		if nameKey(alias) != current && !slices.ContainsFunc(aliases, func(v string) bool { return nameKey(v) == nameKey(alias) }) {
			aliases = append(aliases, alias)
		}
	}
	for _, alias := range old.Name.RetiredAliases {
		add(alias)
	}
	changedName := old.Name.Current != item.Name.Current
	if changedName {
		add(old.Name.Current)
	}
	item.Name = &NameValue{Current: item.Name.Current, RetiredAliases: aliases}
	changed := changedName || !slices.Equal(old.Name.RetiredAliases, aliases)
	evidence := slices.Clone(old.Evidence)
	for _, e := range item.Evidence {
		if !slices.ContainsFunc(evidence, func(v Evidence) bool { return v.Kind == e.Kind && v.ID == e.ID && v.Revision == e.Revision }) {
			evidence = append(evidence, e)
			changed = true
		}
	}
	if !changed {
		return old, nil
	}
	item.ID, item.Revision, item.Evidence = old.ID, old.Revision+1, evidence
	tags := slices.Clone(item.Sensitivity)
	item.Sensitivity = slices.Clone(old.Sensitivity)
	for _, tag := range tags {
		if !slices.Contains(item.Sensitivity, tag) {
			item.Sensitivity = append(item.Sensitivity, tag)
		}
	}
	raw, err = json.Marshal(item)
	if err != nil {
		return Item{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory_item SET content=?,normalized=?,record=? WHERE id=?`, item.Content, normalize(item.Content), raw, item.ID); err != nil {
		return Item{}, err
	}
	if err = revision(ctx, tx, item, raw); err != nil {
		return Item{}, err
	}
	if changedName {
		correction := item
		correction.ID, correction.Revision, correction.Kind, correction.Name = "", 1, Correction, nil
		correction.Evidence = append(slices.Clone(item.Evidence), Evidence{Kind: MemoryItem, ID: old.ID, Revision: old.Revision, Precision: Exact})
		if _, err = insertRecord(ctx, tx, correction); err != nil {
			return Item{}, err
		}
	}
	return item, nil
}

// Names is complete within the authorized scope; it is not a relevance search.
// Keyset pages preserve the existing recall filters without a silent item cap.
func (s *Store) Names(ctx context.Context, scope RecallScope, at int64) ([]Hit, error) {
	var out []Hit
	q := Query{Kind: Name, At: at, Limit: 100}
	for {
		hits, err := s.Recall(ctx, scope, q)
		if err != nil {
			return nil, err
		}
		out = append(out, hits...)
		if len(hits) < q.Limit {
			return out, nil
		}
		q.After = hits[len(hits)-1].ID
	}
}
