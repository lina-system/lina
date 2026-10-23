// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
)

type testStore struct {
	*canon.Store
	Definitions testDefinitions
}

func testJournal(t *testing.T) (*Journal, *testStore) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	wrapped := &testStore{db, testDefinitions{repo}}
	return NewJournal(wrapped), wrapped
}

func TestJournalConcurrentTransitionsLoseNoReservations(t *testing.T) {
	j, _ := testJournal(t)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			err := j.Update(t.Context(), Transition{ID: fmt.Sprint(i), Kind: "slot.reserved", At: 1}, func(v *View) error {
				count := 0
				if r, ok := v.Get("node", "n"); ok {
					if err := json.Unmarshal(r.Body, &count); err != nil {
						return err
					}
				}
				body, _ := json.Marshal(count + 1)
				return v.Put("node", "n", body)
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	rows, err := j.Records(t.Context(), "node")
	if err != nil || len(rows) != 1 || string(rows[0].Body) != "12" || rows[0].Version != 12 {
		t.Fatalf("lost a reservation: %+v %v", rows, err)
	}
}

func TestJournalCommitReplayRollbackAndRebuild(t *testing.T) {
	j, db := testJournal(t)
	event := Transition{ID: "admit:g", Kind: "goal.admitted", At: 1, Data: json.RawMessage(`{"goal":"g"}`)}
	calls := 0
	apply := func(v *View) error {
		calls++
		return v.Put("goal", "g", json.RawMessage(`{"done":"proof","stage":"ready"}`))
	}
	if err := j.Update(t.Context(), event, apply); err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.Wake():
	default:
		t.Fatal("no committed wake")
	}
	if err := j.Update(t.Context(), event, apply); err != nil || calls != 1 {
		t.Fatalf("replay applied: calls=%d err=%v", calls, err)
	}
	event.Data = json.RawMessage(`{"goal":"other"}`)
	if err := j.Update(t.Context(), event, apply); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
	event.ID = "failed"
	err := j.Update(t.Context(), event, func(v *View) error { _ = v.Put("goal", "g", json.RawMessage(`{}`)); return errors.New("rollback") })
	if err == nil {
		t.Fatal("accepted failed transition")
	}
	select {
	case <-j.Wake():
		t.Fatal("rollback/replay woke controller")
	default:
	}
	before, err := j.Records(t.Context(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Version != 1 {
		t.Fatalf("bad projection: %+v", before)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT OR REPLACE INTO work_events(seq,id,kind,at,body)
 SELECT seq,'replacement',kind,at,body FROM work_events WHERE id='admit:g'`)
		return err
	}); err == nil {
		t.Fatal("sequence collision replaced the journal's source event")
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error { _, e := tx.ExecContext(t.Context(), "DELETE FROM work_records"); return e }); err != nil {
		t.Fatal(err)
	}
	reopened := NewJournal(db)
	if err = reopened.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := reopened.Records(t.Context(), "goal")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || string(after[0].Body) != string(before[0].Body) || after[0].Version != before[0].Version {
		t.Fatalf("rebuild changed state: %+v", after)
	}
}

func TestReconcilerStartupCommitAndSafetySweep(t *testing.T) {
	j, _ := testJournal(t)
	ticks := make(chan time.Time)
	passes := make(chan int, 10)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := StartReconciler(ctx, j, func(ctx context.Context) error {
		rows, err := j.Records(ctx, "goal")
		if err == nil {
			passes <- len(rows)
		}
		return err
	}, ticks)
	t.Cleanup(func() { cancel(); <-r.Done() })
	expect := func(want int) {
		t.Helper()
		select {
		case got := <-passes:
			if got != want {
				t.Fatalf("pass has %d goals, want %d", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("reconcile did not run")
		}
	}
	expect(0)
	if err := j.Update(t.Context(), Transition{ID: "g", Kind: "goal.admitted", At: 1}, func(v *View) error { return v.Put("goal", "g", json.RawMessage(`{}`)) }); err != nil {
		t.Fatal(err)
	}
	expect(1)
	// Deliberately lose the process-local wake: startup and the shared sweep
	// derive from persisted state, not from a remembered action queue.
	ticks <- time.Unix(300, 0)
	expect(1)
}

type testDefinitions struct{ repo *records.Store }

func (s testDefinitions) Read(ctx context.Context, id string) (DefinitionRecord, string, error) {
	r, rev, err := s.repo.Read(ctx, id)
	if errors.Is(err, records.ErrNotFound) {
		err = ErrDefinitionMissing
	}
	return DefinitionRecord{ID: r.ID, Path: r.Path, Data: r.Data}, rev, err
}
func (s testDefinitions) Commit(ctx context.Context, c DefinitionChange, person, agent string) (string, error) {
	return s.repo.Commit(ctx, records.Change{ID: c.ID, Base: c.Base, Path: c.Path, Data: c.Data}, records.Actor{Person: person, Agent: agent})
}
