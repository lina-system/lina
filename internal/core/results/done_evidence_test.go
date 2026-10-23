// SPDX-License-Identifier: Apache-2.0
package results

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
)

func TestCompletionEvidenceRevisionsAndCounter(t *testing.T) {
	t.Parallel()
	s, db, _, _ := setup(t)
	p := proposal()
	p.Completion = &result.CompletionRequest{WorkKind: "document"}
	first, err := s.Present(t.Context(), scope, p)
	if err != nil || first.Completion == nil || first.Completion.State != "claimed" {
		t.Fatalf("claim: %+v %v", first, err)
	}
	p.ID, p.BaseRevision, p.Completion = first.ID, first.Revision, nil
	second, err := s.Present(t.Context(), scope, p)
	if err != nil || second.Completion == nil || second.Completion.State != "claimed" {
		t.Fatalf("omitted proof hid claim: %+v %v", second, err)
	}
	ctx := result.WithCompletionPolicy(t.Context(), func(req result.CompletionRequest) result.Completion {
		return result.AssessCompletion(req, nil, func(ref result.EvidenceReference) bool { return ref.TurnID == "turn" && ref.CallID == "read" })
	})
	p.BaseRevision = second.Revision
	p.Completion = &result.CompletionRequest{WorkKind: "document", Evidence: []result.EvidenceReference{{Kind: "source_reopened", TurnID: "turn", CallID: "read"}}}
	third, err := s.Present(ctx, scope, p)
	if err != nil || third.Completion == nil || third.Completion.State != "done" {
		t.Fatalf("verified: %+v %v", third, err)
	}
	read, err := s.Read(t.Context(), scope, result.ReadRequest{ID: first.ID})
	if err != nil || read.Completion.State != "done" || read.Title != p.Title || read.Summary != p.Summary {
		t.Fatalf("read: %+v %v", read, err)
	}
	if _, err = s.Present(ctx, scope, p); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale write: %v", err)
	}
	var count int
	err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT breaches FROM agent_done_breaches WHERE agent_id=?", scope.AgentID).Scan(&count)
	})
	if err != nil || count != 2 {
		t.Fatalf("counter: %d %v", count, err)
	}
}
