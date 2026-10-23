// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestCodingCloseIntentBlocksWithoutVerifiedAndRejectsStaleHead(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "stale"}[stale], func(t *testing.T) {
			r, a, d := conformanceFixture(t)
			rr, err := r.beginConformance(t.Context(), a, &d)
			if err != nil {
				t.Fatal(err)
			}
			rr.State, rr.Verdict, rr.Evidence = "complete", "needs_changes", "Missing behavior."
			if err = r.finishConformance(t.Context(), a, rr); err != nil {
				t.Fatal(err)
			}
			if stale {
				if err := r.work.ObserveHead(t.Context(), a.ID, "run", strings.Repeat("d", 40)); err != nil {
					t.Fatal(err)
				}
			}
			err = r.saveClose(t.Context(), a, rr, "closing", "P1 gap: Missing behavior.")
			if stale {
				if !errors.Is(err, work.ErrState) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			card, err := r.work.Get(t.Context(), a.ID)
			if err != nil || card.Verdict != "" || card.Applied || card.State != "input_needed" {
				t.Fatalf("false acceptance: %+v %v", card, err)
			}
			if err := r.saveClose(t.Context(), a, rr, "closing", "P1 gap: Missing behavior."); !errors.Is(err, work.ErrState) {
				t.Fatal("duplicate write intent", err)
			}
		})
	}
}

func TestGoalDeliveryHoldsCorrectionAndLeavesMergeToGoal(t *testing.T) {
	for _, verdict := range []string{"verified", "needs_changes"} {
		t.Run(verdict, func(t *testing.T) {
			r, a, d := conformanceFixture(t)
			d.Goal, d.State = true, "finished"
			rr, err := r.beginConformance(t.Context(), a, &d)
			if err != nil {
				t.Fatal(err)
			}
			rr.State, rr.Verdict, rr.Evidence = "complete", verdict, "Definition checked."
			if err := r.finishConformance(t.Context(), a, rr); err != nil {
				t.Fatal(err)
			}
			if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
				raw, _ := json.Marshal(d)
				_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", d.ID, "turn", "/repo", d.State, raw)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			r.workerDispatch, err = dispatch.Open(t.Context(), dispatch.Config{DB: r.db, Capacity: func(context.Context) int { return 0 }})
			if err != nil {
				t.Fatal(err)
			}
			defer r.workerDispatch.Close(context.Background())
			got, err := r.goalDeliveryObservation(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			if verdict == "needs_changes" {
				if got.Status != goal.OutcomeRunning || got.Head != "" || got.EffectsSettled || got.Handle != d.ID {
					t.Fatalf("old head accepted before correction: %+v", got)
				}
			} else if got.Status != goal.OutcomeSucceeded || got.Head != d.PR.Head || !got.EffectsSettled {
				t.Fatalf("verified receipt lost: %+v", got)
			}
			card, err := r.work.Get(t.Context(), a.ID)
			if err != nil || card.Applied {
				t.Fatal("worker action merged ahead of goal merge action", err)
			}
		})
	}
}

func TestCodingInterruptedCloseObservesWithoutRepeatingWrite(t *testing.T) {
	r, a, d := conformanceFixture(t)
	rr, err := r.beginConformance(t.Context(), a, &d)
	if err != nil {
		t.Fatal(err)
	}
	rr.State, rr.Verdict, rr.Evidence = "complete", "needs_changes", "Missing behavior."
	if err = r.finishConformance(t.Context(), a, rr); err != nil {
		t.Fatal(err)
	}
	if err = r.saveClose(t.Context(), a, rr, "closing", rr.Evidence); err != nil {
		t.Fatal(err)
	}
	closedGitHubFixture(t, r, rr.Head, rr.Evidence)
	for i := 0; i < 2; i++ {
		state, err := r.deliverCoding(t.Context(), a, d)
		if err != nil || state != "closed" {
			t.Fatalf("reconcile=%s %v", state, err)
		}
	}
	card, err := r.work.Get(t.Context(), a.ID)
	if err != nil || card.Verdict != "" || card.State != "input_needed" {
		t.Fatalf("card=%+v %v", card, err)
	}
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error { a, err = readCoding(t.Context(), tx, a.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if a.Delivery.State != "closed" {
		t.Fatal(a.Delivery)
	}
	saved, err := review.New(r.db).Get(t.Context(), rr.Key)
	if err != nil || saved.Verdict != "needs_changes" {
		t.Fatal("original finding evidence lost", saved, err)
	}
}

func TestCodingUnchangedCorrectionCloses(t *testing.T) {
	r, a, d := conformanceFixture(t)
	rr, err := r.beginConformance(t.Context(), a, &d)
	if err != nil {
		t.Fatal(err)
	}
	rr.State, rr.Verdict, rr.Evidence = "complete", "needs_changes", "P1 gap: Missing behavior."
	if err := r.finishConformance(t.Context(), a, rr); err != nil {
		t.Fatal(err)
	}
	card, err := r.work.Get(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.ReturnReceipt = card.Receipt.EventID
	run := *card.Run
	run.ID = "correction"
	run.Generation++
	run.TurnID = "correction-turn"
	if err := r.work.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	receipt := *card.Receipt
	receipt.ExecutionGeneration = run.Generation
	receipt.Attempt++
	receipt.TurnRef.TurnID = run.TurnID
	receipt.EventID = work.ReceiptID(receipt)
	if err := r.work.Submit(t.Context(), run.ID, receipt); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error { return work.FinishIn(t.Context(), tx, a.ID, run.ID, "completed", d.PR.Head) }); err != nil {
		t.Fatal(err)
	}
	closedGitHubFixture(t, r, rr.Head, rr.Evidence)
	state, err := r.deliverCoding(t.Context(), a, d)
	if err != nil || state != "closed" {
		t.Fatalf("unchanged corrected head not closed: %s %v", state, err)
	}
	card, err = r.work.Get(t.Context(), a.ID)
	if err != nil || card.Verdict != "" || card.State != "input_needed" {
		t.Fatalf("residual not blocked: %+v %v", card, err)
	}
}

func TestCodingFreshFailureOverridesSavedVerifiedJudgment(t *testing.T) {
	r, a, d := conformanceFixture(t)
	rr, err := r.beginConformance(t.Context(), a, &d)
	if err != nil {
		t.Fatal(err)
	}
	rr.State, rr.Verdict, rr.Evidence = "complete", "verified", "Definition met."
	if err := r.finishConformance(t.Context(), a, rr); err != nil {
		t.Fatal(err)
	}
	d.FailureReason = "P1 later-finding: Required behavior regressed."
	d.Code = "pr_unverified"
	closedGitHubFixture(t, r, rr.Head, d.FailureReason)
	state, err := r.deliverCoding(t.Context(), a, d)
	if err != nil || state != "closed" {
		t.Fatalf("fresh failure lost behind old verdict: %s %v", state, err)
	}
}

func closedGitHubFixture(t *testing.T, r *conversationRuntime, head, reason string) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "gh")
	pr, _ := json.Marshal(map[string]any{"number": 7, "state": "closed", "merged": false, "head": map[string]string{"sha": head}, "base": map[string]any{"ref": "dev", "repo": map[string]string{"full_name": "owner/repo"}}})
	comments, _ := json.Marshal([]map[string]string{{"body": reason}})
	script := "#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\n[ \"$1\" = api ] || exit 99\ncase \"$*\" in *'/issues/7/comments'*) printf 'HTTP/2.0 200 OK\\r\\n\\r\\n%s\\n' '" + string(comments) + "';; *) printf 'HTTP/2.0 200 OK\\r\\n\\r\\n%s\\n' '" + string(pr) + "';; esac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	r.workerGitHub, err = ghclient.New(t.Context(), ghclient.Config{Path: binary, Leaser: lifecycle.NewRegistry(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.workerGitHub.Close(context.Background()) })
}
