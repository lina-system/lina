// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func fixture(t *testing.T) (*canon.Store, *Store, *retract.Store, canon.Config) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removals.Close() })
	addEvent(t, db, "e1", "conversation")
	addEvent(t, db, "e2", "conversation")
	return db, New(db), removals, cfg
}
func addEvent(t *testing.T, db *canon.Store, id, conversation string) {
	t.Helper()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: conversation, PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(20, 0), TimeQuality: "observed", Body: "synthetic " + id})
	if err != nil {
		t.Fatal(err)
	}
}
func frontier(t *testing.T, s *Store) Frontier {
	t.Helper()
	f, err := s.Frontier(t.Context(), "conversation")
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func output(text string) Output {
	return Output{Model: "test-model", PromptVersion: "v1", BuiltAt: time.Unix(30, 0), Text: text, Dropped: "other details"}
}
func proposal(t *testing.T, s *Store, id string) Proposal {
	return Proposal{Expected: frontier(t, s), ID: id, Cover: []Coverage{{EventID: "e1"}}, Verbatim: []string{"e2"}, Output: output("summary e1")}
}
func publish(t *testing.T, s *Store, p Proposal) Node {
	t.Helper()
	n, err := s.Publish(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
func counts(t *testing.T, db *canon.Store) []int {
	t.Helper()
	var out []int
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		for _, table := range []string{"sum_node", "sum_edge", "sum_cover"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				return err
			}
			out = append(out, n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestPublicationRejectsStaleFrontierAndEpoch(t *testing.T) {
	for _, kind := range []string{"append", "competing publish", "epoch"} {
		t.Run(kind, func(t *testing.T) {
			db, s, _, _ := fixture(t)
			p := proposal(t, s, "candidate")
			want := ErrStaleFrontier
			switch kind {
			case "append":
				addEvent(t, db, "e3", "conversation")
			case "competing publish":
				publish(t, s, proposal(t, s, "winner"))
			case "epoch":
				write(t, db, "UPDATE retract_meta SET epoch=epoch+1")
				want = ErrStaleEpoch
			}
			before := counts(t, db)
			if n, err := s.Publish(t.Context(), p); !errors.Is(err, want) || n.ID != "" {
				t.Fatalf("publish=%+v %v", n, err)
			}
			if got := counts(t, db); !reflect.DeepEqual(got, before) {
				t.Fatalf("partial publication %v != %v", got, before)
			}
		})
	}
}
func TestPublicationCoverageAndTree(t *testing.T) {
	db, s, _, cfg := fixture(t)
	for _, kind := range []string{"gap", "duplicate", "foreign", "invented skip", "hidden verbatim"} {
		t.Run(kind, func(t *testing.T) {
			p := proposal(t, s, "invalid")
			switch kind {
			case "gap":
				p.Verbatim = nil
			case "duplicate":
				p.Verbatim = append(p.Verbatim, "e1")
			case "foreign":
				p.Cover[0].EventID = "absent"
			case "invented skip":
				p.Cover[0].Reason = "replay"
			case "hidden verbatim":
				p.Cover[0].Reason = "retracted"
			}
			if _, err := s.Publish(t.Context(), p); !errors.Is(err, ErrCoverage) {
				t.Fatalf("coverage accepted: %v", err)
			}
		})
	}
	leaf := publish(t, s, proposal(t, s, "leaf"))
	parent := publish(t, s, Proposal{Expected: frontier(t, s), ID: "parent", Children: []string{leaf.ID}, Cover: []Coverage{{EventID: "e2"}}, Output: output("parent text")})
	if parent.Level != 1 || !parent.StartAt.Equal(time.Unix(10, 0)) || parent.Epoch != 0 || !reflect.DeepEqual(parent.Children, []string{"leaf"}) {
		t.Fatalf("parent=%+v", parent)
	}
	if got := counts(t, db); !reflect.DeepEqual(got, []int{2, 1, 2}) {
		t.Fatal(got)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	reopened, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	got, err := New(reopened).Node(t.Context(), "parent")
	if err != nil || !reflect.DeepEqual(got, parent) {
		t.Fatalf("reopen=%+v %v", got, err)
	}
}
func write(t *testing.T, db *canon.Store, sql string, args ...any) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error { _, err := tx.ExecContext(t.Context(), sql, args...); return err }); err != nil {
		t.Fatal(err)
	}
}
func forget(t *testing.T, db *canon.Store, r *retract.Store) {
	t.Helper()
	receipt, err := memoryexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: "synthetic preference", Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "e1", Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
}
func TestRetractionRebuildsLeafAndAncestorsAtomically(t *testing.T) {
	db, s, r, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	publish(t, s, Proposal{Expected: frontier(t, s), ID: "parent", Children: []string{"leaf"}, Cover: []Coverage{{EventID: "e2"}}, Output: output("parent e1 e2")})
	forget(t, db, r)
	for _, id := range []string{"leaf", "parent"} {
		if _, err := s.Node(t.Context(), id); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("stale %s: %v", id, err)
		}
	}
	err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sum_node WHERE text<>'' OR dropped<>''").Scan(&n)
		if n != 0 {
			t.Fatal("invalidated content retained")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("summarizer failed")
	var calls []string
	summarize := func(_ context.Context, in Segment) (Output, error) {
		calls = append(calls, in.NodeID)
		if in.NodeID == "parent" {
			return Output{}, failed
		}
		return output("rebuilt leaf"), nil
	}
	if _, err := s.Rebuild(t.Context(), "conversation", summarize); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if _, err := s.Node(t.Context(), "leaf"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("partial rebuild committed", err)
	}
	calls = nil
	n, err := s.Rebuild(t.Context(), "conversation", func(_ context.Context, in Segment) (Output, error) {
		calls = append(calls, in.NodeID)
		if in.NodeID == "leaf" && len(in.Events) != 0 {
			t.Fatalf("forgotten input: %+v", in)
		}
		if in.NodeID == "parent" && (len(in.Children) != 1 || in.Children[0].Output.Text != "rebuilt leaf" || !reflect.DeepEqual(in.Events, []string{"e2"})) {
			t.Fatalf("parent input: %+v", in)
		}
		return output("rebuilt " + in.NodeID), nil
	})
	if err != nil || n.Epoch != 1 || n.Revision != 2 || !reflect.DeepEqual(calls, []string{"leaf", "parent"}) {
		t.Fatalf("rebuild %+v %v calls=%v", n, err, calls)
	}
	leaf, err := s.Node(t.Context(), "leaf")
	if err != nil || leaf.Cover[0].Reason != "retracted" {
		t.Fatalf("leaf=%+v %v", leaf, err)
	}
	// The forgotten event remains in the lossless ledger, not in summary input.
	e, err := ledger.New(db, nil).Get(t.Context(), "e1")
	if err != nil || e.Body.Text() != "synthetic e1" {
		t.Fatalf("ledger=%+v %v", e, err)
	}
}
func TestRebuildRejectsConcurrentAppendWithoutPartialWrites(t *testing.T) {
	db, s, r, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	forget(t, db, r)
	_, err := s.Rebuild(t.Context(), "conversation", func(context.Context, Segment) (Output, error) {
		addEvent(t, db, "late", "conversation")
		return output("rebuilt"), nil
	})
	if !errors.Is(err, ErrStaleFrontier) {
		t.Fatal(err)
	}
	if _, err := s.Node(t.Context(), "leaf"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestPublicationRollsBackStorageFailure(t *testing.T) {
	db, s, _, _ := fixture(t)
	write(t, db, `CREATE TRIGGER fail_cover BEFORE INSERT ON sum_cover BEGIN SELECT RAISE(ABORT,'injected'); END;`)
	if _, err := s.Publish(t.Context(), proposal(t, s, "failed")); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatal(err)
	}
	if got := fmt.Sprint(counts(t, db)); got != "[0 0 0]" {
		t.Fatal(got)
	}
}

func TestRebuildFencesEpochAndCompetingRebuild(t *testing.T) {
	for _, kind := range []string{"epoch", "rebuild"} {
		t.Run(kind, func(t *testing.T) {
			db, s, r, _ := fixture(t)
			publish(t, s, proposal(t, s, "leaf"))
			forget(t, db, r)
			want := ErrStaleEpoch
			_, err := s.Rebuild(t.Context(), "conversation", func(context.Context, Segment) (Output, error) {
				if kind == "epoch" {
					write(t, db, "UPDATE retract_meta SET epoch=epoch+1")
				} else {
					want = ErrStaleFrontier
					if _, err := s.Rebuild(t.Context(), "conversation", func(context.Context, Segment) (Output, error) { return output("winner"), nil }); err != nil {
						t.Fatal(err)
					}
				}
				return output("loser"), nil
			})
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
			n, err := s.Node(t.Context(), "leaf")
			if kind == "epoch" {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatal(err)
				}
			} else if err != nil || n.Output.Text != "winner" || n.Revision != 2 {
				t.Fatalf("winner overwritten: %+v %v", n, err)
			}
		})
	}
}

func TestInvalidChildrenAndVerbatimCannotHideRevocation(t *testing.T) {
	db, s, r, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	// Even an otherwise complete proposal cannot discard the established root.
	p := Proposal{Expected: frontier(t, s), ID: "fork", Cover: []Coverage{{EventID: "e1"}, {EventID: "e2"}}, Output: output("fork")}
	if _, err := s.Publish(t.Context(), p); !errors.Is(err, ErrCoverage) {
		t.Fatal(err)
	}
	write(t, db, "UPDATE sum_node SET status='degraded' WHERE id='leaf'")
	p.Children = []string{"leaf"}
	p.Cover = p.Cover[1:]
	if _, err := s.Publish(t.Context(), p); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	write(t, db, "UPDATE sum_node SET status='ready' WHERE id='leaf'")
	forget(t, db, r)
	p.Expected = frontier(t, s)
	if _, err := s.Publish(t.Context(), p); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	// A conversation with no previous root must still account for hidden input.
	addEvent(t, db, "other", "other-conversation")
	write(t, db, `INSERT INTO removal_applied(sequence,id,digest) VALUES(100,'synthetic-removal','digest');
 INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id)
 VALUES('event','other','person','','agent','erased',1,'{}','synthetic-removal');
 UPDATE retract_meta SET epoch=epoch+1;`)
	f, err := s.Frontier(t.Context(), "other-conversation")
	if err != nil {
		t.Fatal(err)
	}
	p = Proposal{Expected: f, ID: "hidden", Cover: []Coverage{{EventID: "other"}}, Output: output("invalid")}
	if _, err := s.Publish(t.Context(), p); !errors.Is(err, ErrCoverage) {
		t.Fatal(err)
	}
	p.Cover[0].Reason = "retracted"
	n := publish(t, s, p)
	if n.Cover[0].Reason != "retracted" {
		t.Fatal(n)
	}
}

func TestRebuildRollsBackDatabaseFailure(t *testing.T) {
	db, s, r, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	publish(t, s, Proposal{Expected: frontier(t, s), ID: "parent", Children: []string{"leaf"}, Cover: []Coverage{{EventID: "e2"}}, Output: output("parent")})
	forget(t, db, r)
	write(t, db, `CREATE TRIGGER fail_rebuild BEFORE UPDATE ON sum_node WHEN NEW.id='parent' AND NEW.status='ready' BEGIN SELECT RAISE(ABORT,'injected'); END;`)
	if _, err := s.Rebuild(t.Context(), "conversation", func(context.Context, Segment) (Output, error) { return output("rebuilt"), nil }); err == nil {
		t.Fatal("fault ignored")
	}
	for _, id := range []string{"leaf", "parent"} {
		if _, err := s.Node(t.Context(), id); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("partial rebuild: %s %v", id, err)
		}
	}
}

func TestPublicationRecordsSkipsAndRejectsIncompleteOutput(t *testing.T) {
	db, s, _, _ := fixture(t)
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "copy", ConversationID: "conversation", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(40, 0), RecordedAt: time.Unix(40, 0), TimeQuality: "observed", Body: "synthetic e1", ReplayOf: "e1"})
	if err != nil {
		t.Fatal(err)
	}
	p := proposal(t, s, "leaf")
	p.Cover = append(p.Cover, Coverage{EventID: "copy", Reason: "replay"})
	for _, bad := range []Output{{}, {Text: "partial"}, output(" "), output("sk-proj-" + strings.Repeat("Synthetic", 5))} {
		q := p
		q.Output = bad
		if _, err := s.Publish(t.Context(), q); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	n := publish(t, s, p)
	if n.Cover[1].Reason != "replay" || !n.EndAt.Equal(time.Unix(40, 0)) {
		t.Fatal(n)
	}
}
