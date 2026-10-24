// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestGoalSentencesDoNotDecodeExecutionGraph(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("first"))
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	read := func(engine *Engine) {
		titles, err := engine.GoalSentences(t.Context())
		if err != nil || len(titles) != 1 || titles[g.ID] != g.Summary {
			t.Fatalf("canonical title lost: %v, %v", titles, err)
		}
	}
	before := testing.AllocsPerRun(3, func() { read(e) })
	if err := e.journal.Update(t.Context(), Transition{ID: "many-steps", Kind: "fixture", At: 100}, func(v *View) error {
		current, err := loadGoal(v, g.ID)
		if err != nil {
			return err
		}
		for i := range 82 {
			current.Tasks = append(current.Tasks, readyTask(fmt.Sprintf("task-%d", i)))
		}
		return put(v, "goal", current.ID, current)
	}); err != nil {
		t.Fatal(err)
	}
	after := testing.AllocsPerRun(3, func() { read(e) })
	t.Logf("title lookup: %.0f allocations for one step, %.0f for 83", before, after)
	if after > 2*before {
		t.Fatalf("title lookup decodes execution state: %.0f -> %.0f allocations", before, after)
	}
	// Restarted readers have no cached definition and must still recover the
	// same title from its canonical record, with the saved owner and revision.
	restarted, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	read(restarted)
}

func TestGoalDefinitionLivesInRecordsNotExecutionLedger(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.Summary = "synthetic unique outcome"
	g.Scope.Includes = "synthetic unique boundary"
	g.Decisions = []GoalDecision{{Statement: "synthetic unique decision", MessageID: "person-message"}}
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	saved := s.Goals[0]
	if saved.RecordID == "" || saved.RecordRevision == "" || saved.Summary != g.Summary {
		t.Fatalf("missing canonical goal record: %+v", saved)
	}
	r, rev, err := db.Definitions.Read(t.Context(), saved.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if rev != saved.RecordRevision || !strings.Contains(string(r.Data), g.Summary) {
		t.Fatal("record does not contain the goal definition")
	}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"work_events", "work_records"} {
			rows, err := tx.QueryContext(t.Context(), "SELECT body FROM "+table)
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				for _, private := range []string{g.Summary, g.Scope.Includes, g.Decisions[0].Statement} {
					if strings.Contains(raw, private) {
						t.Errorf("goal prose duplicated in %s", table)
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRecordEditFencesGoalUntilManagerInterpretsRevision(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	g := oneGoal(readyTask("a"))
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Snapshot(t.Context())
	saved := before.Goals[0]
	r, rev, err := db.Definitions.Read(t.Context(), saved.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nChanged outcome from the person.\n")...)}, g.Person, g.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := e.Snapshot(t.Context())
	updated := after.Goals[0]
	if updated.RecordRevision != edited || updated.Revision != saved.Revision+1 || !updated.DefinitionPending {
		t.Fatalf("record edit ignored: %+v", updated)
	}
	if len(after.Actions) != 1 || after.Actions[0].Kind != PlanGoal {
		t.Fatalf("stale goal dispatched: %+v", after.Actions)
	}
}

// pausedDefinitionCommit holds only the first write before the canon transaction,
// allowing an adapter report to commit while a record refresh is in flight.
type pausedDefinitionCommit struct {
	Canon
	armed            atomic.Bool
	entered, release chan struct{}
}

func (p *pausedDefinitionCommit) Write(ctx context.Context, f func(*canon.Tx) error) error {
	if p.armed.CompareAndSwap(true, false) {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.Canon.Write(ctx, f)
}

func TestRecordRefreshKeepsInflightReportDefinition(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	g := oneGoal(readyTask("a"))
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	r, rev, err := db.Definitions.Read(t.Context(), s.Goals[0].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nA corrected scope.\n")...)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	paused := &pausedDefinitionCommit{Canon: db, entered: make(chan struct{}), release: make(chan struct{})}
	paused.armed.Store(true)
	e.journal.db = paused
	finished := make(chan error, 1)
	go func() { finished <- e.RefreshDefinitions(t.Context()) }()
	<-paused.entered
	// These must keep using the committed revision, even after the new record
	// has been read. No timing-based scheduling is needed to reproduce the race.
	if err = e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeSucceeded, Head: "built-head", EffectsSettled: true}); err != nil {
		t.Errorf("in-flight report lost its definition: %v", err)
	}
	if _, err = e.Snapshot(t.Context()); err != nil {
		t.Errorf("snapshot lost committed definition: %v", err)
	}
	close(paused.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Goals[0].Tasks[0].Head != "built-head" {
		t.Fatal("record correction lost the completed worker result")
	}
}

func TestUnplannedRecordCorrectionStaysBlockedAcrossRestart(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	cfg := Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }}
	e, _ := NewEngine(cfg)
	g := oneGoal(readyTask("a"))
	if err := e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r, rev, err := db.Definitions.Read(t.Context(), s.Goals[0].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nThe scope has changed.\n")...)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Actions) != 1 || s.Actions[0].Kind != PlanGoal {
		t.Fatalf("expected one correction plan: %+v", s.Actions)
	}
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if i == 1 {
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = e.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		s, err = e.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if s.Goals[0].State != GoalBlocked || s.Goals[0].AttentionID == "" {
			t.Errorf("unplanned correction did not stay blocked: %+v", s.Goals[0])
		}
		for _, action := range s.Actions {
			if action.State != "finished" {
				t.Errorf("stale work resumed without a corrected plan: %+v", action)
			}
		}
	}
	// A later person correction can open a fresh manager turn.
	r, rev, err = db.Definitions.Read(t.Context(), s.Goals[0].RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Definitions.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nHere is the missing decision.\n")...)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plans := 0
	for _, action := range s.Actions {
		if action.Kind == PlanGoal && action.State == "queued" {
			plans++
		}
	}
	if plans != 1 {
		t.Fatalf("new correction did not reopen one manager turn: %+v", s.Actions)
	}
}
