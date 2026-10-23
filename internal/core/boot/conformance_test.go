// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func conformanceFixture(t *testing.T) (*conversationRuntime, codingAssignment, dispatch.Record) {
	t.Helper()
	return conformanceFixtureWithOutcome(t, "ready_for_review")
}

func conformanceFixtureWithOutcome(t *testing.T, outcome string) (*conversationRuntime, codingAssignment, dispatch.Record) {
	t.Helper()
	r, _, origin, _ := managementFixture(t)
	owner, err := r.agents.Create(t.Context(), agents.Create{Name: "developer", PersonID: r.person, Preset: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	a := codingAssignment{ID: "task", Manager: "manager", Owner: owner.ID, Conversation: "manager:fixture", Origin: dispatch.Origin{Person: r.person, Agent: origin}, Delegated: true}
	p := antigravity.PullRequest{Number: 7, URL: "https://github.com/owner/repo/pull/7", State: "OPEN", Head: strings.Repeat("a", 40), Branch: "task", Base: "dev", Checks: []antigravity.Check{{Type: "CheckRun", Name: "foundation", Status: "COMPLETED", Conclusion: "SUCCESS"}}}
	rr := antigravity.Review{State: "APPROVED"}
	rr.Commit.OID = p.Head
	p.Reviews = []antigravity.Review{rr}
	d := dispatch.Record{ID: a.ID, Origin: a.Origin, Owner: a.Owner, Manager: a.Conversation, Code: "ready_for_review", PR: &p, BaseCommit: strings.Repeat("b", 40), ChangedFiles: []string{"src/a.go"}, DesignText: map[string]string{"design.md#result": "Base contract"}, ChangeEvidence: "diff", Definition: dispatch.Definition{Branch: "task", Base: "dev", WriteArea: []string{"src/**"}}}
	a.Arguments, _ = json.Marshal(d.Definition)
	r.work = work.New(r.db)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO coding_managers(id,person,owner,unit,conversation) VALUES(?,?,?,?,?)", a.Manager, r.person, a.Owner, "repo", a.Conversation); err != nil {
			return err
		}
		raw, _ := json.Marshal(a)
		_, err := tx.ExecContext(t.Context(), "INSERT INTO coding_assignments(id,manager,record) VALUES(?,?,?)", a.ID, a.Manager, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.work.Create(t.Context(), work.Task{ID: a.ID, PersonID: r.person, AgentID: a.Owner, ConversationID: a.Conversation, Title: "Change", Done: "Result works", ArtifactRoots: []string{"/work/task"}, RequiresApply: true}); err != nil {
		t.Fatal(err)
	}
	run := work.Run{ID: "run", TaskID: a.ID, Generation: 1, Worker: "fixture", ThreadID: "thread", TurnID: "turn", NodeID: "main"}
	if err := r.work.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: a.ID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: "/work/task/result", SHA256: strings.Repeat("a", 64)}}, Provenance: []work.Provenance{{ArtifactID: "artifact", TaskID: a.ID, NodeID: run.NodeID, Path: "/work/task/result", SHA256: strings.Repeat("a", 64), ObservedAt: now}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.Outcome = outcome
	receipt.EventID = work.ReceiptID(receipt)
	if err := r.work.Submit(t.Context(), run.ID, receipt); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error { return work.FinishIn(t.Context(), tx, a.ID, run.ID, "completed", p.Head) }); err != nil {
		t.Fatal(err)
	}
	return r, a, d
}

func TestConformanceUnavailableEvidenceBlocksBeforeScopeVerdict(t *testing.T) {
	for _, kind := range []string{"code", "base", "diff", "design", "paths", "pr_unverified", "checks"} {
		t.Run(kind, func(t *testing.T) {
			r, a, d := conformanceFixtureWithOutcome(t, "blocked_needs_input")
			d.ChangedFiles = []string{"outside.txt"}
			cause := "change_evidence_unavailable"
			switch kind {
			case "code":
				d.Code = "change_evidence_unavailable"
			case "base":
				d.BaseCommit = ""
			case "diff":
				d.ChangeEvidence = ""
			case "design":
				d.DesignText = nil
			case "paths":
				d.ChangedFiles = nil
			case "pr_unverified":
				d.Code = "pr_unverified"
				cause = "merge_precondition"
			case "checks":
				d.PR.Checks[0].Conclusion = "FAILURE"
				d.ChangeEvidence = ""
				cause = "merge_precondition"
			}
			// A mechanical block must finish even without a provider or router.
			callErr := r.runCodingManager(t.Context(), a, "delivery", &d)
			got, err := review.New(r.db).Get(t.Context(), review.Key{Repository: "owner/repo", Number: 7, Head: d.PR.Head})
			if callErr != nil || err != nil || got.State != "blocked" || got.Cause != cause || got.Verdict != "" || len(got.Findings) != 0 || got.Evidence == "" {
				t.Fatalf("delivery error=%v stored review=%+v read error=%v", callErr, got, err)
			}
			card, err := r.work.Get(t.Context(), a.ID)
			if err != nil || card.State != "input_needed" || card.Verdict != "" || card.Applied || got.Receipt != card.Receipt.EventID || got.Generation != card.Run.Generation {
				t.Fatalf("blocked delivery lost: card=%+v review=%+v error=%v", card, got, err)
			}
			if err = r.db.Read(t.Context(), func(tx *canon.Tx) error { a, err = readCoding(t.Context(), tx, a.ID); return err }); err != nil {
				t.Fatal(err)
			}
			if a.Verdict != "" || a.VerdictHead != got.Head || a.VerdictReceipt != got.Receipt || r.conformanceVerified(t.Context(), a, d) {
				t.Fatalf("assignment did not retain the blocked head: %+v", a)
			}
			if err = r.runCodingManager(t.Context(), a, "delivery", &d); !errors.Is(err, review.ErrDuplicate) {
				t.Fatalf("duplicate check=%v", err)
			}
		})
	}
}

func TestConformanceMechanicalDecisionsSkipManagerAndRejectDuplicate(t *testing.T) {
	for _, kind := range []string{"scope", "checks"} {
		t.Run(kind, func(t *testing.T) {
			r, a, d := conformanceFixture(t)
			verdict := "needs_changes"
			if kind == "scope" {
				d.ChangedFiles = append(d.ChangedFiles, "outside.txt")
			} else {
				d.PR.Checks[0].Conclusion = "FAILURE"
				verdict = ""
			}
			// No provider route or router exists: mechanical decisions must not use one.
			if err := r.runCodingManager(t.Context(), a, "delivery", &d); err != nil {
				t.Fatal(err)
			}
			card, err := r.work.Get(t.Context(), a.ID)
			if err != nil || card.Verdict != verdict || card.Applied {
				t.Fatalf("card=%+v error=%v", card, err)
			}
			if kind == "checks" && card.State != "input_needed" {
				t.Fatalf("CI did not block: %+v", card)
			}
			got, err := review.New(r.db).Get(t.Context(), review.Key{Repository: "owner/repo", Number: 7, Head: d.PR.Head})
			if err != nil || len(got.Findings) != 0 || got.Verdict != verdict {
				t.Fatalf("mechanical result=%+v %v", got, err)
			}
			if err := r.runCodingManager(t.Context(), a, "delivery", &d); !errors.Is(err, review.ErrDuplicate) {
				t.Fatalf("duplicate check=%v", err)
			}
		})
	}
}

func TestConformanceFixGenerationChecksNewHeadWithoutReusingVerdict(t *testing.T) {
	r, a, d := conformanceFixture(t)
	d.ChangedFiles = []string{"outside.txt"}
	if err := r.runCodingManager(t.Context(), a, "delivery", &d); err != nil {
		t.Fatal(err)
	}
	oldHead := d.PR.Head
	card, err := r.work.Get(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	run := *card.Run
	run.ID = "fix-run"
	run.Generation++
	run.TurnID = "fix-turn"
	if err = r.work.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	receipt := *card.Receipt
	receipt.ExecutionGeneration = run.Generation
	receipt.Attempt++
	receipt.TurnRef.TurnID = run.TurnID
	receipt.EventID = work.ReceiptID(receipt)
	if err = r.work.Submit(t.Context(), run.ID, receipt); err != nil {
		t.Fatal(err)
	}
	d.PR.Head = strings.Repeat("c", 40)
	d.PR.Reviews[0].Commit.OID = d.PR.Head
	d.ChangedFiles = []string{"src/a.go"}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error { return work.FinishIn(t.Context(), tx, a.ID, run.ID, "completed", d.PR.Head) }); err != nil {
		t.Fatal(err)
	}
	rr, err := r.beginConformance(t.Context(), a, &d)
	if err != nil || rr.State != "checking" || rr.Generation != 2 {
		t.Fatalf("new head did not get a check: %+v %v", rr, err)
	}
	rr.State = "complete"
	rr.Verdict = "verified"
	rr.Evidence = "The corrected result meets the definition."
	if err = r.finishConformance(t.Context(), a, rr); err != nil {
		t.Fatal(err)
	}
	if err = r.db.Read(t.Context(), func(tx *canon.Tx) error { a, err = readCoding(t.Context(), tx, a.ID); return err }); err != nil {
		t.Fatal(err)
	}
	if !r.conformanceVerified(t.Context(), a, d) {
		t.Fatal("new head judgment unavailable")
	}
	d.PR.Head = oldHead
	if r.conformanceVerified(t.Context(), a, d) {
		t.Fatal("old head inherited fix judgment")
	}
	old, err := review.New(r.db).Get(t.Context(), review.Key{Repository: "owner/repo", Number: 7, Head: oldHead})
	if err != nil || old.Verdict != "needs_changes" {
		t.Fatalf("old judgment overwritten: %+v %v", old, err)
	}
}
func TestConformanceLateDecisionRollsBackWithStaleHead(t *testing.T) {
	r, a, d := conformanceFixture(t)
	rr, err := r.beginConformance(t.Context(), a, &d)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.work.ObserveHead(t.Context(), a.ID, "run", strings.Repeat("c", 40)); err != nil {
		t.Fatal(err)
	}
	rr.State = "complete"
	rr.Verdict = "verified"
	rr.Evidence = "Old observation"
	if err = r.finishConformance(t.Context(), a, rr); !errors.Is(err, work.ErrState) {
		t.Fatalf("stale decision=%v", err)
	}
	got, err := review.New(r.db).Get(t.Context(), rr.Key)
	if err != nil || got.State != "checking" || got.Verdict != "" {
		t.Fatalf("partial transaction: %+v %v", got, err)
	}
	card, err := r.work.Get(t.Context(), a.ID)
	if err != nil || card.Verdict != "" {
		t.Fatal("stale work verdict", err)
	}
}
