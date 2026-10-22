// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerDispatchUsesTransferGateAndHoldsRollback(t *testing.T) {
	s, _ := testStore(t)
	dir := t.TempDir()
	task := taskFixture()
	task.PersonID = "person-1"
	task.ExternalControl = true
	task.ArtifactRoots = []string{dir}
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveWorker(t.Context(), "worker", link); !errors.Is(err, ErrControlHeld) {
		t.Fatal("unapproved live dispatch", err)
	}
	if err := s.ApproveSwitch(t.Context(), switchFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", "disabled and stopped"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveWorker(t.Context(), "worker", link); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestRollback(t.Context(), "switch-1", "person-1", "failed check"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckWorker(t.Context(), "worker"); !errors.Is(err, ErrControlHeld) {
		t.Fatal("steering during rollback", err)
	}
	if err := s.CompleteRollback(t.Context(), "switch-1", "person-1", "drained"); !errors.Is(err, ErrActiveRun) {
		t.Fatal("worker was not counted", err)
	}
	if err := s.Recover(t.Context(), "restart"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRollback(t.Context(), "switch-1", "person-1", "drained"); !errors.Is(err, ErrActiveRun) {
		t.Fatal("restart forgot worker", err)
	}
	if err := s.ReleaseWorker(t.Context(), "worker"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteRollback(t.Context(), "switch-1", "person-1", "drained"); err != nil {
		t.Fatal(err)
	}
}

func TestExternalTaskImportRefusesAnExistingWorker(t *testing.T) {
	s, _ := testStore(t)
	dir := t.TempDir()
	if err := s.ReserveWorker(t.Context(), "existing-worker", dir); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	task := taskFixture()
	task.PersonID = "person-1"
	task.ExternalControl = true
	task.ArtifactRoots = []string{alias}
	for _, restart := range []bool{false, true} {
		if restart {
			if err := s.Recover(t.Context(), "restart"); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Create(t.Context(), task); !errors.Is(err, ErrActiveRun) {
			t.Fatalf("external task adopted an existing worker directory (restart=%v): %v", restart, err)
		}
		if _, err := s.Get(t.Context(), task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("refused import retained a task", err)
		}
	}
	if err := s.ReleaseWorker(t.Context(), "existing-worker"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveWorker(t.Context(), "next-worker", dir); !errors.Is(err, ErrControlHeld) {
		t.Fatal("imported task was not gated", err)
	}
	if err := s.ApproveSwitch(t.Context(), switchFixture()); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateSwitch(t.Context(), "switch-1", "person-1", "disabled and stopped"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReserveWorker(t.Context(), "next-worker", dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(t.Context(), task); err != nil {
		t.Fatal("identical import retry after activation", err)
	}
}
