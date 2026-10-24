// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestCandidateOccupiedPaths(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		name := "normal"
		if interrupted {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			s, root, db := setup(t)
			occupied := []Change{
				{ID: "first", Path: "inbox/candidate-1.md", Data: doc("first", "Candidate 1")},
				{ID: "second", Path: "inbox/candidate-1-2.md", Data: doc("second", "Candidate 1 2")},
			}
			for i := range occupied {
				occupied[i].Base = commit(t, s, occupied[i])
			}
			in := proposal("source", "Send notes")
			seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
			if interrupted {
				s.PublicationHook = func(stage string) error {
					if stage == "file" {
						return errors.New("interrupted candidate")
					}
					return nil
				}
			}
			added, err := s.ProposeCandidate(t.Context(), actor, in)
			if (!interrupted && err != nil) || (interrupted && !errors.Is(err, ErrUncertain)) {
				t.Fatalf("candidate with occupied paths: %v", err)
			}
			recovered, err := Open(root, db)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			got, err := recovered.ProposeCandidate(t.Context(), actor, in)
			if err != nil || got.Number != 1 || got.Status != "added" || got.RecordID == "" || got.Undo == "" || got.Notice == "" {
				t.Fatal("retry lost candidate receipt", got, err)
			}
			if !interrupted && got != added {
				t.Fatal("retry changed candidate", got, added)
			}
			r, _, err := recovered.Read(t.Context(), got.RecordID)
			if err != nil || r.Path == occupied[0].Path || r.Path == occupied[1].Path {
				t.Fatal("candidate path collision", r.Path, err)
			}
			rows, err := recovered.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
			if err != nil || len(rows) != 1 || rows[0].ID != got.RecordID {
				t.Fatal("duplicate addition", rows, err)
			}
			if _, err = recovered.UndoRecord(t.Context(), actor, got.Undo); err != nil {
				t.Fatal(err)
			}
			for _, want := range occupied {
				r, rev, err := recovered.Read(t.Context(), want.ID)
				if err != nil || r.Path != want.Path || !bytes.Equal(r.Data, want.Data) || rev != want.Base {
					t.Fatal("occupied record changed", r, rev, err)
				}
			}
		})
	}
}

func seedCandidateSource(t *testing.T, db Canon, id, body, person string, entry guard.Entry) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := ledger.New(db, nil).Admit(t.Context(), entry, ledger.Input{ID: id, PersonID: person, AgentID: "agent", ConversationID: "main:agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: body, Locator: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
}
func proposal(source, title string) CandidateInput {
	return CandidateInput{Source: source, Title: title, Quotation: "I will " + title + ".", Confidence: 0.9}
}

func TestCandidateDeterministicGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, person, body, quote, title string
		entry                            guard.Entry
		added                            bool
	}{
		{"owner-promise", "person", "I will Send the notes.", "I will Send the notes.", "Send the notes", guard.EntryOwner, true},
		{"owner-request", "person", "Remind me to Send the notes.", "Remind me to Send the notes.", "Send the notes", guard.EntryOwner, true},
		{"korean-owner", "person", "내가 결과 정리할게.", "내가 결과 정리할게.", "결과 정리하기", guard.EntryOwner, true},
		{"other-owner", "other", "I will Send the notes.", "I will Send the notes.", "Send the notes", guard.EntryOwner, false},
		{"agent-claim", "", "I will Send the notes.", "I will Send the notes.", "Send the notes", guard.EntryAgent, false},
		{"automation", "", "I will Send the notes.", "I will Send the notes.", "Send the notes", guard.EntryAutomation, false},
		{"not-a-request", "person", "The notes are ready.", "The notes are ready.", "Send the notes", guard.EntryOwner, false},
		{"forged-quote", "person", "The notes are ready.", "I will Send the notes.", "Send the notes", guard.EntryOwner, false},
		{"unrelated-task", "person", "I will Read the notes.", "I will Read the notes.", "Send the notes", guard.EntryOwner, false},
		{"negation", "person", "I will not Send the notes.", "I will not Send the notes.", "not Send the notes", guard.EntryOwner, false},
		{"quoted-fragment", "person", "Someone wrote: I will Send the notes.", "I will Send the notes.", "Send the notes", guard.EntryOwner, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, db := setup(t)
			seedCandidateSource(t, db, "source", tc.body, tc.person, tc.entry)
			in := CandidateInput{Source: "source", Quotation: tc.quote, Title: tc.title, Confidence: 1}
			got, err := s.ProposeCandidate(t.Context(), actor, in)
			if err != nil || (got.Status == "added") != tc.added || got.Number == 0 {
				t.Fatal(got, err)
			}
			if tc.added && (got.Undo == "" || got.Notice == "" || got.SourceRevision == "") {
				t.Fatal("missing admission evidence", got)
			}
			rows, err := s.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
			if err != nil || (len(rows) == 1) != tc.added {
				t.Fatal(rows, err)
			}
		})
	}
}
func TestCandidateConcurrentEquivalentsAndClassRejections(t *testing.T) {
	t.Parallel()
	s, root, db := setup(t)
	in := proposal("source-0", "Send the notes")
	seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Go(func() {
			if _, err := s.ProposeCandidate(t.Context(), actor, in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	list, err := s.Candidates(t.Context(), actor.Person, 0)
	if err != nil || len(list) != 1 || list[0].Status != "added" {
		t.Fatal(list, err)
	}
	duplicate := proposal("equivalent", "send the notes")
	seedCandidateSource(t, db, duplicate.Source, duplicate.Quotation, actor.Person, guard.EntryOwner)
	if c, err := s.ProposeCandidate(t.Context(), actor, duplicate); err != nil || c.Status != "pending" {
		t.Fatal("open equivalent", c, err)
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			in = proposal(fmt.Sprintf("source-%d", i), fmt.Sprintf("Send notes %d", i))
			seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
		}
		c, err := s.ProposeCandidate(t.Context(), actor, in)
		if err != nil || c.Status != "added" {
			t.Fatal(c, err)
		}
		if _, err = s.UndoRecord(t.Context(), actor, c.Undo); err != nil {
			t.Fatal(err)
		}
		replay, err := s.ProposeCandidate(t.Context(), actor, in)
		if err != nil || replay.Status != "rejected" {
			t.Fatal("undo replay", replay, err)
		}
	}
	recovered, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	in = proposal("source-4", "Send final notes")
	seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
	if c, err := recovered.ProposeCandidate(t.Context(), actor, in); err != nil || c.Status != "pending" {
		t.Fatal("fallback lost", c, err)
	}
	foreign, err := recovered.Candidates(t.Context(), "other", 0)
	if err != nil || len(foreign) != 0 {
		t.Fatal(foreign, err)
	}
}
func TestCandidateCrashDoesNotDuplicateAddition(t *testing.T) {
	t.Parallel()
	s, root, db := setup(t)
	in := proposal("source", "Send notes")
	seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
	s.PublicationHook = func(at string) error {
		if at == "file" {
			return errors.New("interrupted")
		}
		return nil
	}
	if _, err := s.ProposeCandidate(t.Context(), actor, in); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	recovered, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	c, err := recovered.ProposeCandidate(t.Context(), actor, in)
	if err != nil || c.Status != "added" || c.Undo == "" {
		t.Fatal(c, err)
	}
	rows, err := recovered.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}
func TestCandidateMissingSourceCannotBeAutoAdded(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	c, err := s.ProposeCandidate(t.Context(), actor, proposal("missing", "Send notes"))
	if err != nil || c.Status != "pending" {
		t.Fatalf("unverified source was auto-added: %+v %v", c, err)
	}
}
func TestCandidateSourceRecheckedInsidePublicationFence(t *testing.T) {
	t.Parallel()
	s, _, db := setup(t)
	in := proposal("source", "Send notes")
	seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
	s.PublicationHook = func(at string) error {
		if at != "lock" {
			return nil
		}
		return db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_block(item_id,person,holder,event_id) VALUES('forgotten','person','agent','source')")
			return err
		})
	}
	c, err := s.ProposeCandidate(t.Context(), actor, in)
	if err != nil || c.Status != "pending" {
		t.Fatal("stale source admitted", c, err)
	}
	s.PublicationHook = nil
	rows, err := s.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestCandidateReplayAliasCannotRestoreUndoneSource(t *testing.T) {
	t.Parallel()
	s, _, db := setup(t)
	in := proposal("source", "Send notes")
	seedCandidateSource(t, db, in.Source, in.Quotation, actor.Person, guard.EntryOwner)
	c, err := s.ProposeCandidate(t.Context(), actor, in)
	if err != nil || c.Status != "added" {
		t.Fatal(c, err)
	}
	if _, err = s.UndoRecord(t.Context(), actor, c.Undo); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "replay", PersonID: actor.Person, AgentID: actor.Agent, ConversationID: "main:agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: in.Quotation, ReplayOf: in.Source})
	if err != nil {
		t.Fatal(err)
	}
	in.Source = "replay"
	got, err := s.ProposeCandidate(t.Context(), actor, in)
	if err != nil || got.Status != "pending" {
		t.Fatal("replay resurrected undo", got, err)
	}
}
