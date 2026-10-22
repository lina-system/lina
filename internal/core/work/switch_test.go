// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func switchFixture() Switch {
	return Switch{ID: "switch-1", Owner: "person-1", Approval: "approval-1", Snapshot: strings.Repeat("a", 64), Tasks: []string{"task-1"}}
}
func TestSwitchRequiresApprovalAndFencesExecution(t *testing.T) {
	s, _ := testStore(t)
	task := taskFixture()
	task.PersonID = "person-1"
	task.ExternalControl = true
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Switch){func(v *Switch) { v.Approval = "" }, func(v *Switch) { v.Owner = "other" }, func(v *Switch) { v.Snapshot = "" }, func(v *Switch) { v.Tasks = []string{"missing"} }} {
		v := switchFixture()
		mutate(&v)
		if err := s.ApproveSwitch(t.Context(), v); err == nil {
			t.Fatal("invalid approval accepted")
		}
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", "stop-proof"); err == nil {
		t.Fatal("started without approval")
	}
	if err := s.ApproveSwitch(t.Context(), switchFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), runFixture()); !errors.Is(err, ErrControlHeld) {
		t.Fatalf("external owner allowed LINA run: %v", err)
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "other", "stop-proof"); err == nil {
		t.Fatal("second owner took control")
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", ""); err == nil {
		t.Fatal("no quiescence proof accepted")
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", "stop-proof"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), runFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRollback(t.Context(), "switch-1", "person-1", "failed validation"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRollback(t.Context(), "switch-1", "person-1", "drained-proof"); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("released active execution: %v", err)
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "failed"); err != nil {
		t.Fatal(err)
	}
	before, err := s.Ledger(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRollback(t.Context(), "switch-1", "person-1", "drained-proof"); err != nil {
		t.Fatal(err)
	}
	next := runFixture()
	next.ID = "run-2"
	next.Generation = 2
	if err := s.Start(t.Context(), next); !errors.Is(err, ErrControlHeld) {
		t.Fatalf("rolled back task started: %v", err)
	}
	after, err := s.Ledger(t.Context(), "task-1")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rollback changed work facts", err)
	}
	if err := s.Recover(t.Context(), "new-epoch"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), next); !errors.Is(err, ErrControlHeld) {
		t.Fatalf("restart lost ownership: %v", err)
	}
	record, err := s.GetSwitch(t.Context(), "switch-1")
	if err != nil || record.State != "rolled_back" || len(record.Events) != 4 {
		t.Fatalf("missing switch history: %+v %v", record, err)
	}
}

func TestSwitchSerializesOwnersAndPreservesReceipt(t *testing.T) {
	s, _ := testStore(t)
	task := taskFixture()
	task.PersonID = "person-1"
	task.ExternalControl = true
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	// Competing procedures cannot acquire the same task even in separate calls.
	if err := s.ApproveSwitch(t.Context(), switchFixture()); err != nil {
		t.Fatal(err)
	}
	competitor := switchFixture()
	competitor.ID = "other"
	if err := s.ApproveSwitch(t.Context(), competitor); err == nil {
		t.Fatal("second procedure admitted")
	}
	if err := s.ApproveSwitch(t.Context(), switchFixture()); err != nil {
		t.Fatal("identical approval retry", err)
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", "proof"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context(), runFixture()); err != nil {
		t.Fatal(err)
	}
	receipt := receiptFixture()
	if err := s.Submit(t.Context(), "run-1", receipt); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), "task-1", "run-1", "completed"); err != nil {
		t.Fatal(err)
	}
	before, err := s.Get(t.Context(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRollback(t.Context(), "switch-1", "person-1", "validation failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRollback(t.Context(), "switch-1", "person-1", "drained"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Get(t.Context(), "task-1")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rollback changed receipt or projection", err)
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", "proof"); err == nil {
		t.Fatal("stale activation resurrected control")
	}
}
