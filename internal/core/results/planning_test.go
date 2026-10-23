// SPDX-License-Identifier: Apache-2.0
package results

import (
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/result"
	"testing"
)

func TestPlanOnlyLatestConfirmationCreatesRecords(t *testing.T) {
	t.Parallel()
	s, db, _, _ := setup(t)
	repo, err := records.Open(t.TempDir(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	plans := NewPlanning(s, repo)
	root, err := plans.Accept(t.Context(), scope, "goal", "Deliver workshop", "Workshop delivered")
	if err != nil {
		t.Fatal(err)
	}
	input, err := plans.Input(t.Context(), scope, root.ID, "", 0, "Prepare a plan")
	if err != nil {
		t.Fatal(err)
	}
	draft := jobapi.PlanDraft{Title: "Workshop plan", Summary: "Prepare workshop", Entries: []jobapi.PlanEntry{{ID: "project", Kind: "project", Title: "Workshop", Done: "Ready"}, {ID: "todo", Kind: "todo", Title: "Discarded task", Parent: "project", Done: "Reserved"}}}
	apply := func(in jobapi.PlanInput, d jobapi.PlanDraft) (result.Card, error) {
		var c result.Card
		raw, _ := json.Marshal(d)
		err := db.Write(t.Context(), func(tx *canon.Tx) error {
			var err error
			c, err = plans.DraftTx(t.Context(), tx, scope, in, raw)
			return err
		})
		return c, err
	}
	first, err := apply(input, draft)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := repo.Head(t.Context())
	if head != root.Revision {
		t.Fatal("draft wrote plan records")
	}
	input.ResultID, input.BaseRevision = first.ID, first.Revision
	draft.Entries = draft.Entries[:1]
	second, err := apply(input, draft)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Read(t.Context(), scope, result.ReadRequest{ID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	var body result.Plan
	json.Unmarshal(r.Body, &body)
	if len(body.Entries) != 2 {
		t.Fatal("replacement merged deleted entries", string(r.Body))
	}
	if _, err = plans.Confirm(t.Context(), scope, first.ID, first.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatal("stale confirmation", err)
	}
	other := scope
	other.PersonID = "other"
	if _, err = plans.Confirm(t.Context(), other, second.ID, second.Revision); err == nil {
		t.Fatal("cross-owner confirmation")
	}
	receipt, err := plans.Confirm(t.Context(), scope, second.ID, second.Revision)
	if err != nil || receipt.State != "applied" || len(receipt.Records) != 1 {
		t.Fatal(receipt, err)
	}
	again, err := plans.Confirm(t.Context(), scope, second.ID, second.Revision)
	if err != nil || again.Revision != receipt.Revision {
		t.Fatal(again, err)
	}
	if head, _ = repo.Head(t.Context()); head != receipt.Revision {
		t.Fatal("repeat wrote again")
	}
	if _, err = apply(jobapi.PlanInput{RootID: root.ID, RootRevision: root.Revision, Kind: "goal", Title: input.Title, Done: input.Done, ResultID: first.ID, BaseRevision: second.Revision}, draft); err == nil {
		t.Fatal("confirmed plan replaced")
	}
	if count(t, db, "plan_confirmations") != 1 {
		t.Fatal("second confirmation record")
	}
}

func TestPlanConfirmationReconcilesInterruptedPublication(t *testing.T) {
	t.Parallel()
	s, db, _, _ := setup(t)
	repo, err := records.Open(t.TempDir(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	plans := NewPlanning(s, repo)
	root, err := plans.Accept(t.Context(), scope, "project", "Ship a report", "Report delivered")
	if err != nil {
		t.Fatal(err)
	}
	input, err := plans.Input(t.Context(), scope, root.ID, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"title":"Report plan","summary":"Write the report","entries":[{"id":"write","kind":"todo","title":"Write report","parent":"","done":"Draft available"}]}`)
	var card result.Card
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		card, err = plans.DraftTx(t.Context(), tx, scope, input, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Present(t.Context(), scope, result.PresentRequest{ID: card.ID, BaseRevision: 1, Type: result.PlanType, Title: current.Title, Summary: current.Summary, Body: current.Body}); !errors.Is(err, ErrOutOfScope) {
		t.Fatal("generic present bypassed managed draft", err)
	}
	repo.PublicationHook = func(stage string) error {
		if stage == "ref" {
			return errors.New("cut")
		}
		return nil
	}
	receipt, err := plans.Confirm(t.Context(), scope, card.ID, 1)
	if !errors.Is(err, records.ErrUncertain) || receipt.State != "unknown" {
		t.Fatal(receipt, err)
	}
	repo.PublicationHook = nil
	recovered, err := plans.Confirm(t.Context(), scope, card.ID, 1)
	if err != nil || recovered.State != "applied" {
		t.Fatal(recovered, err)
	}
	current, err = s.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	var plan result.Plan
	json.Unmarshal(current.Body, &plan)
	if plan.Confirm != nil {
		t.Fatal("confirmed plan still offers confirmation")
	}
	// Handoff tasks are execution records referencing the already confirmed todo.
	// Creating them never opens or consumes another plan confirmation.
	engine := work.New(db)
	for _, id := range []string{"write-part", "verify-part"} {
		err = engine.Create(t.Context(), work.Task{ID: id, GoalID: recovered.Records[0], PersonID: scope.PersonID, AgentID: scope.AgentID, ConversationID: scope.ConversationID, Title: id, Done: "Verified artifact"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if count(t, db, "plan_confirmations") != 1 || count(t, db, "work_cards") != 2 {
		t.Fatal("task splitting requires another confirmation")
	}
}
