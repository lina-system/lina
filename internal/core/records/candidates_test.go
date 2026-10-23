// SPDX-License-Identifier: Apache-2.0
package records

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestCandidateDeterministicGate(t *testing.T) {
	for bits := 0; bits < 8; bits++ {
		s, _, _ := setup(t)
		input := CandidateInput{Source: "event:source", SourceRevision: "1", Class: "promise", Title: "Send the notes", Confidence: 0.99}
		facts := CandidateFacts{AddressedToPerson: bits&1 != 0, FromPerson: bits&2 != 0, RequestOrPromise: bits&4 != 0}
		c, err := s.ProposeCandidate(t.Context(), actor, input, facts)
		if err != nil {
			t.Fatal(err)
		}
		if (c.Status == "added") != (bits == 7) || c.Number == 0 || c.Source != input.Source {
			t.Fatalf("bits=%d candidate=%+v", bits, c)
		}
		rows, err := s.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
		if err != nil || (len(rows) == 1) != (bits == 7) {
			t.Fatal(bits, rows, err)
		}
		if bits == 7 {
			if c.Undo == "" || c.Notice == "" {
				t.Fatal("automatic addition lacks undo", c)
			}
			replay, err := s.ProposeCandidate(t.Context(), actor, input, facts)
			if err != nil || replay.Number != c.Number || replay.RecordID != c.RecordID {
				t.Fatal("duplicate source", replay, err)
			}
			input.Source = "event:equivalent"
			duplicate, err := s.ProposeCandidate(t.Context(), actor, input, facts)
			if err != nil || duplicate.Status != "pending" {
				t.Fatal("open equivalent", duplicate, err)
			}
			if _, err = s.UndoRecord(t.Context(), actor, c.Undo); err != nil {
				t.Fatal(err)
			}
			input.Source = c.Source
			rejected, err := s.ProposeCandidate(t.Context(), actor, input, facts)
			if err != nil || rejected.Status != "rejected" {
				t.Fatal("undone source reappeared", rejected, err)
			}
		}
	}
}

func TestCandidateConcurrentEquivalentsAndClassRejections(t *testing.T) {
	s, root, db := setup(t)
	facts := CandidateFacts{true, true, true}
	input := CandidateInput{Source: "event:1", SourceRevision: "1", Class: "promise", Title: "Send the notes", Confidence: 0.8}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Go(func() {
			if _, err := s.ProposeCandidate(t.Context(), actor, input, facts); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	list, err := s.Candidates(t.Context(), actor.Person, 0)
	if err != nil || len(list) != 1 || list[0].Status != "added" {
		t.Fatal(list, err)
	}
	for i := 0; i < 3; i++ {
		input.Source = fmt.Sprintf("event:%d", i+1)
		input.Title = fmt.Sprintf("Send notes %d", i)
		if i == 0 {
			input.Title = "Send the notes"
		}
		c, err := s.ProposeCandidate(t.Context(), actor, input, facts)
		if err != nil || c.Status != "added" {
			t.Fatal(c, err)
		}
		if _, err = s.UndoRecord(t.Context(), actor, c.Undo); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	input.Source = "event:4"
	input.Title = "Send final notes"
	c, err := recovered.ProposeCandidate(t.Context(), actor, input, facts)
	if err != nil || c.Status != "pending" {
		t.Fatal("rejection fallback lost", c, err)
	}
	foreign, err := recovered.Candidates(t.Context(), "other", 0)
	if err != nil || len(foreign) != 0 {
		t.Fatal("foreign candidates", foreign, err)
	}
}

func TestCandidateCrashDoesNotDuplicateAddition(t *testing.T) {
	s, root, db := setup(t)
	s.PublicationHook = func(at string) error {
		if at == "file" {
			return errors.New("interrupted")
		}
		return nil
	}
	input := CandidateInput{Source: "event:1", SourceRevision: "1", Class: "request", Title: "Send notes", Confidence: 1}
	facts := CandidateFacts{true, true, true}
	if _, err := s.ProposeCandidate(t.Context(), actor, input, facts); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	recovered, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	c, err := recovered.ProposeCandidate(t.Context(), actor, input, facts)
	if err != nil || c.Status != "added" || c.Undo == "" {
		t.Fatal(c, err)
	}
	rows, err := recovered.Query(t.Context(), Params{Person: actor.Person, Kind: "todo"})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
}
