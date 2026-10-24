// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// QuoteReader is supplied by the composing flow. Stores do not import each other.
type QuoteReader func(context.Context, string, string, canon.Citation) (canon.CitationValue, error)

func (s *Store) WithQuotes(read QuoteReader) *Store {
	copy := *s
	copy.quotes = read
	return &copy
}

// marshalRecord stores the item's fields once per revision. Provenance lives in cite.
func marshalRecord(item Item) ([]byte, error) {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "evidence")
	return json.Marshal(fields)
}

// DecodeItem reconstitutes the requested revision's provenance from canon.
func DecodeItem(ctx context.Context, tx *canon.Tx, raw []byte, item *Item) error {
	if err := json.Unmarshal(raw, item); err != nil {
		return err
	}
	evidence, err := EvidenceAt(ctx, tx, "memory", item.ID, item.Revision)
	item.Evidence = evidence
	return err
}

func EvidenceAt(ctx context.Context, tx *canon.Tx, kind, id string, revision int64) ([]Evidence, error) {
	citations, err := canon.CitationsAt(ctx, tx, kind, id, revision)
	if err != nil {
		return nil, err
	}
	out := make([]Evidence, 0, len(citations))
	for _, c := range citations {
		k := EvidenceKind(c.TargetKind)
		if k == "memory" {
			k = MemoryItem
		}
		rev, err := strconv.ParseInt(c.TargetRevision, 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, Evidence{Kind: k, ID: c.TargetID, Revision: rev, Precision: Precision(c.Precision), SourceAgent: c.SourceAgent, OccurredAt: c.OccurredAt})
	}
	return out, nil
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
