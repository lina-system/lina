// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

type codingDelivery struct {
	State, Head, Receipt, Reason string
}

// deliverCoding connects the manager's one definition return to the existing
// dispatcher and merge service. A saved review is read, never requested again.
func (r *conversationRuntime) deliverCoding(ctx context.Context, a codingAssignment, d dispatch.Record) (string, error) {
	if d.PR == nil {
		return "unverified", nil
	}
	repo, _, ok := strings.Cut(strings.TrimPrefix(d.PR.URL, "https://github.com/"), "/pull/")
	if !ok {
		return "unverified", work.ErrInvalid
	}
	key := review.Key{Repository: repo, Number: d.PR.Number, Head: d.PR.Head}
	rr, err := review.New(r.db).Get(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		err = r.runCodingManager(ctx, a, "delivery", &d)
		if err != nil && !errors.Is(err, errConformanceUnverified) {
			return "unverified", err
		}
		rr, err = review.New(r.db).Get(ctx, key)
	}
	if err != nil {
		return "unverified", err
	}
	if err = r.db.Read(ctx, func(tx *canon.Tx) error { a, err = readCoding(ctx, tx, a.ID); return err }); err != nil {
		return "unverified", err
	}
	card, err := r.work.Get(ctx, a.ID)
	if err != nil || card.Receipt == nil || card.Head != rr.Head || card.CompletionHead != rr.Head || rr.Task != a.ID || card.Active {
		return "unverified", work.ErrState
	}
	if card.Receipt.EventID != rr.Receipt {
		// Returning the identical commit cannot resolve its previously observed
		// definition miss. Keep that review intact and close against the new
		// receipt, without a second review request for the same head.
		if d.ReturnReceipt != rr.Receipt || rr.Verdict != "needs_changes" || card.Run == nil || card.Run.Generation <= rr.Generation {
			return "unverified", work.ErrState
		}
		rr.Receipt, rr.Generation = card.Receipt.EventID, card.Run.Generation
	}
	if strings.HasPrefix(d.Origin.Turn, "turn:a2a-input:") {
		if rr.State == "complete" && rr.Verdict == "verified" && d.FailureReason == "" {
			return "verified", nil
		}
		return "unverified", nil
	}
	if card.Applied {
		return "merged", nil
	}
	if a.Delivery.State == "closed" && a.Delivery.Receipt == rr.Receipt {
		return "closed", nil
	}
	if a.Delivery.State == "closing" && a.Delivery.Receipt == rr.Receipt {
		return r.closeCoding(ctx, a, d, rr, a.Delivery.Reason)
	}
	// A later terminal failure at this head is current evidence even when a
	// previous manager judgment passed before the interrupted merge.
	if d.FailureReason != "" {
		return r.closeCoding(ctx, a, d, rr, d.FailureReason)
	}
	if rr.State == "complete" && rr.Verdict == "needs_changes" {
		payload, err := review.Return(rr)
		if err != nil {
			return "unverified", err
		}
		if d.ReturnReceipt == "" || d.ReturnReceipt == rr.Receipt {
			if r.workerDispatch == nil {
				return "unverified", work.ErrState
			}
			body, _ := json.Marshal(payload)
			correction := dispatch.Correction{Task: rr.Task, Receipt: rr.Receipt, Head: rr.Head, Generation: rr.Generation, Payload: body}
			if err := r.workerDispatch.ReturnDefinition(ctx, d, correction); err != nil {
				return "unverified", err
			}
			return "returned", nil
		}
		return r.closeCoding(ctx, a, d, rr, payload.Reason())
	}
	if rr.State != "complete" || rr.Verdict != "verified" {
		return "unverified", nil
	}
	// Goal execution accepts the worker receipt only after the correction has
	// settled; its existing merge action owns the subsequent external write.
	if d.Goal {
		return "verified", nil
	}
	observation, err := r.mergeGoalPR(ctx, d, repo, goal.Action{Revision: rr.Head}, d.PR.State == "MERGED")
	if err != nil {
		return "merge_pending", err
	}
	if observation.Status != goal.OutcomeSucceeded {
		return "merge_pending", errors.New("merge_confirmation_pending")
	}
	return "merged", nil
}

func (r *conversationRuntime) closeCoding(ctx context.Context, a codingAssignment, d dispatch.Record, rr review.Record, reason string) (string, error) {
	if r.workerGitHub == nil {
		return "blocked", work.ErrState
	}
	masked, err := secrets.Mask(reason)
	if err != nil {
		return "blocked", err
	}
	reason = masked.DerivedText()
	if strings.TrimSpace(reason) == "" {
		return "blocked", work.ErrInvalid
	}
	ref := ghclient.Ref{Repository: rr.Repository, Number: rr.Number}
	var result ghclient.CloseResult
	if a.Delivery.State == "closing" {
		result, err = r.workerGitHub.ObserveClosed(ctx, ref, d.Definition.Base, rr.Head, reason)
	} else {
		result, err = r.workerGitHub.ClosePR(ctx, ref, d.Definition.Base, rr.Head, reason, func(ctx context.Context) error {
			return r.saveClose(ctx, a, rr, "closing", reason)
		})
	}
	if err != nil {
		return "close_pending", err
	}
	if result.State != "closed" {
		return "close_pending", fmt.Errorf("close not confirmed: %s", result.Cause)
	}
	if err = r.saveClose(ctx, a, rr, "closed", reason); err != nil {
		return "close_pending", err
	}
	return "closed", nil
}

func (r *conversationRuntime) saveClose(ctx context.Context, a codingAssignment, rr review.Record, state, reason string) error {
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := readCoding(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if current.Owner != a.Owner || current.Origin != a.Origin {
			return work.ErrState
		}
		if current.Delivery.State != "" && (current.Delivery.Receipt != rr.Receipt || current.Delivery.Head != rr.Head || current.Delivery.Reason != reason) {
			return work.ErrState
		}
		if current.Delivery.State == "closed" || state == "closing" && current.Delivery.State != "" {
			return work.ErrState
		}
		if err := work.BlockDeliveryIn(ctx, tx, a.ID, rr.Receipt, rr.Head, reason); err != nil {
			return err
		}
		current.Verdict = ""
		current.Delivery = codingDelivery{State: state, Head: rr.Head, Receipt: rr.Receipt, Reason: reason}
		return saveCoding(ctx, tx, current)
	})
}
