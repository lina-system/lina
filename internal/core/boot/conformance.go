// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func (r *conversationRuntime) beginConformance(ctx context.Context, a codingAssignment, d *dispatch.Record) (review.Record, error) {
	if d == nil || d.PR == nil || d.ID != a.ID || d.Owner != a.Owner || d.Origin.Person != a.Origin.Person {
		return review.Record{}, work.ErrState
	}
	repo, _, ok := strings.Cut(strings.TrimPrefix(d.PR.URL, "https://github.com/"), "/pull/")
	if !ok || d.PR.URL != fmt.Sprintf("https://github.com/%s/pull/%d", repo, d.PR.Number) {
		return review.Record{}, work.ErrInvalid
	}
	card, err := r.work.Get(ctx, a.ID)
	if err != nil {
		return review.Record{}, err
	}
	if card.Active || card.Receipt == nil || card.Run == nil || card.Head != d.PR.Head || card.CompletionHead != d.PR.Head || card.Task.AgentID != a.Owner || card.Task.PersonID != a.Origin.Person {
		return review.Record{}, work.ErrState
	}
	rr := review.Record{Key: review.Key{Repository: repo, Number: d.PR.Number, Head: d.PR.Head}, Task: a.ID, Receipt: card.Receipt.EventID, Generation: card.Run.Generation, State: "checking"}
	if err = review.New(r.db).Claim(ctx, rr); err != nil {
		return rr, err
	}
	result := review.Assess(d.PR.Verify(repo, d.TaskDefinition().Branch, d.Definition.Base, d.PR.Head), d.ChangedFiles, d.Definition.WriteArea)
	if result.State == "" && d.Code == "pr_unverified" {
		result = review.Record{State: "blocked", Cause: "merge_precondition", Evidence: "The delivered head does not satisfy GitHub merge prerequisites."}
	}
	if result.State == "" && (d.Code != "ready_for_review" || d.BaseCommit == "" || d.ChangeEvidence == "" || len(d.DesignText) == 0 || len(d.ChangedFiles) == 0) {
		result = review.Record{State: "blocked", Cause: "change_evidence_unavailable", Evidence: "The delivered diff and base design are unavailable."}
	}
	if result.State != "" {
		rr.State, rr.Cause, rr.Verdict, rr.Evidence = result.State, result.Cause, result.Verdict, result.Evidence
		err = r.finishConformance(ctx, a, rr)
	}
	return rr, err
}

func (r *conversationRuntime) finishConformance(ctx context.Context, a codingAssignment, rr review.Record) error {
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := readCoding(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if current.Owner != a.Owner || rr.Task != a.ID {
			return work.ErrState
		}
		// FinishIn and the work event either both commit or neither does.
		if err = review.FinishIn(ctx, tx, rr); err != nil {
			return err
		}
		verdict := rr.Verdict
		if rr.State == "unverified" {
			verdict = "unverified"
		}
		if err = work.ConformanceIn(ctx, tx, a.ID, rr.Receipt, rr.Head, verdict, rr.Evidence); err != nil {
			return err
		}
		current.Verdict = rr.Verdict
		current.VerdictHead = rr.Head
		current.VerdictReceipt = rr.Receipt
		return saveCoding(ctx, tx, current)
	})
}

// Reading an existing head judgment for the merge gate is not a new check.
func (r *conversationRuntime) conformanceVerified(ctx context.Context, a codingAssignment, d dispatch.Record) bool {
	if d.PR == nil || a.Verdict != "verified" || a.VerdictHead != d.PR.Head {
		return false
	}
	repo, _, ok := strings.Cut(strings.TrimPrefix(d.PR.URL, "https://github.com/"), "/pull/")
	if !ok {
		return false
	}
	rr, err := review.New(r.db).Get(ctx, review.Key{Repository: repo, Number: d.PR.Number, Head: d.PR.Head})
	card, cardErr := r.work.Get(ctx, a.ID)
	return err == nil && cardErr == nil && rr.State == "complete" && rr.Verdict == "verified" && rr.Task == a.ID && rr.Receipt == a.VerdictReceipt && card.Receipt != nil && card.Receipt.EventID == rr.Receipt && card.Head == rr.Head && card.CompletionHead == rr.Head && card.Verdict == "verified"
}

func conformanceFindings(raw json.RawMessage) ([]review.Finding, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return review.Parse(append(append([]byte(`{"findings":`), raw...), '}'))
}

var errConformanceUnverified = errors.New("coding_delivery_unverified")
