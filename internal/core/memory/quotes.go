// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// QuoteReader is supplied by the composing flow. Stores do not import each other.
type QuoteReader func(context.Context, string, string, canon.Citation) (canon.CitationValue, error)

func (s *Store) WithQuotes(read QuoteReader) *Store {
	copy := *s
	copy.quotes = read
	return &copy
}

// marshalRecord retains the reference metadata, never a rendered quote. In
// particular, an update of a legacy item cannot copy its old quotes forward.
func marshalRecord(item Item) ([]byte, error) {
	item.Evidence = slices.Clone(item.Evidence)
	for i := range item.Evidence {
		item.Evidence[i].Quote = ""
		item.Evidence[i].Status = ""
		item.Evidence[i].Tombstone = nil
	}
	return json.Marshal(item)
}

// Quotes resolves the citations in force at this view revision. It runs after
// the candidate transaction so material adapters may use their own canon reads.
func (s *Store) Quotes(ctx context.Context, scope RecallScope, kind, id string, revision int64, evidence []Evidence) ([]Evidence, error) {
	var citations []canon.Citation
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		citations, err = canon.CitationsAt(ctx, tx, kind, id, revision)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := slices.Clone(evidence)
	for i := range out {
		e := &out[i]
		legacy := e.Quote
		e.Quote, e.Status, e.Tombstone = "", "", nil
		if e.SourceAgent != "" && e.SourceAgent != scope.Agent {
			continue
		}
		if e.Kind != Event {
			// Legacy non-event evidence remains readable until the contract migration.
			e.Quote = legacy
			continue
		}
		e.Status = "bytes_unavailable"
		if s.quotes == nil {
			continue
		}
		for _, c := range citations {
			if c.Ordinal != i || c.TargetKind != "event" || c.TargetID != e.ID {
				continue
			}
			value, err := s.quotes(ctx, scope.Person, scope.Agent, c)
			if err != nil {
				return nil, err
			}
			e.Quote, e.Status, e.Tombstone = value.Quote, value.Status, value.Tombstone
			break
		}
	}
	return out, nil
}
