// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestPublicationRequiresOrderedPrefix(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"interleaved", "suffix", "reversed cover", "reversed tail", "child gap"} {
		t.Run(kind, func(t *testing.T) {
			db, s, _, _ := fixture(t)
			addEvent(t, db, "e3", "conversation")
			p := Proposal{Expected: frontier(t, s), ID: "candidate", Output: output("summary")}
			switch kind {
			case "interleaved":
				p.Cover, p.Verbatim = []Coverage{{EventID: "e1"}, {EventID: "e3"}}, []string{"e2"}
			case "suffix":
				p.Cover, p.Verbatim = []Coverage{{EventID: "e2"}, {EventID: "e3"}}, []string{"e1"}
			case "reversed cover":
				p.Cover, p.Verbatim = []Coverage{{EventID: "e2"}, {EventID: "e1"}}, []string{"e3"}
			case "reversed tail":
				p.Cover, p.Verbatim = []Coverage{{EventID: "e1"}}, []string{"e3", "e2"}
			case "child gap":
				leaf := proposal(t, s, "leaf")
				leaf.Verbatim = []string{"e2", "e3"}
				publish(t, s, leaf)
				p.Expected = frontier(t, s)
				p.Children = []string{"leaf"}
				p.Cover, p.Verbatim = []Coverage{{EventID: "e3"}}, []string{"e2"}
			}
			before := counts(t, db)
			if n, err := s.Publish(t.Context(), p); !errors.Is(err, ErrCoverage) || n.ID != "" {
				t.Errorf("unordered publication: node=%q err=%v", n.ID, err)
			}
			if got := counts(t, db); !reflect.DeepEqual(got, before) {
				t.Errorf("rejected publication changed tables: %v -> %v", before, got)
			}
		})
	}
}

func TestRebuildRestoresSkipReasons(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"replay", "scaffold", "summary_echo", "provider_item"} {
		t.Run(kind, func(t *testing.T) {
			db, s, r, cfg := fixture(t)
			in := ledger.Input{ID: "special", ConversationID: "conversation", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(40, 0), RecordedAt: time.Unix(40, 0), TimeQuality: "observed", Body: "synthetic special"}
			reason := kind
			switch kind {
			case "replay":
				in.ReplayOf = "e1"
			case "scaffold":
				in.ScaffoldKind = "skill_invocation"
			case "summary_echo":
				in.ScaffoldKind = "compaction_recorded"
			case "provider_item":
				in.ScaffoldKind, reason = "provider_item", ""
			}
			if _, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, in); err != nil {
				t.Fatal(err)
			}
			cover := []Coverage{{EventID: "e1"}, {EventID: "e2"}, {EventID: "special", Reason: reason}}
			publish(t, s, Proposal{Expected: frontier(t, s), ID: "leaf", Cover: cover, Output: output("initial")})
			evidence := []memory.Evidence{{Kind: memory.Event, ID: "e1", Precision: memory.Exact}}
			if kind != "replay" {
				evidence = append(evidence, memory.Evidence{Kind: memory.Event, ID: "special", Precision: memory.Exact})
			}
			receipt, err := memoryexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Preference, Content: "synthetic preference", Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Standing: true, Evidence: evidence}})
			if err != nil {
				t.Fatal(err)
			}
			request := retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}
			for cycle := 0; cycle < 2; cycle++ {
				if err := r.Forget(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				n, err := s.Rebuild(t.Context(), "conversation", func(_ context.Context, segment Segment) (Output, error) {
					if !reflect.DeepEqual(segment.Events, []string{"e2"}) {
						t.Errorf("retracted summary inputs: %v", segment.Events)
					}
					return output("without removed evidence"), nil
				})
				if err != nil || n.Cover[0].Reason != "retracted" || n.Cover[2].Reason != "retracted" {
					t.Fatalf("retracted node=%+v err=%v", n, err)
				}
				if err := r.Restore(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				n, err = s.Rebuild(t.Context(), "conversation", func(_ context.Context, segment Segment) (Output, error) {
					want := []string{"e1", "e2"}
					if kind == "provider_item" {
						want = append(want, "special")
					}
					if !reflect.DeepEqual(segment.Events, want) {
						t.Errorf("restored summary inputs: %v, want %v", segment.Events, want)
					}
					return output("restored eligible evidence"), nil
				})
				if err != nil || !reflect.DeepEqual(n.Cover, cover) {
					t.Errorf("restored cover=%+v, want %+v; err=%v", n.Cover, cover, err)
				}
			}
			// Reasons survive reopening and permit a parent to reuse this root.
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			reopened, err := canon.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close(context.Background()) })
			s = New(reopened)
			n, err := s.Node(t.Context(), "leaf")
			if err != nil || !reflect.DeepEqual(n.Cover, cover) {
				t.Fatalf("reopened cover=%+v err=%v", n.Cover, err)
			}
			publish(t, s, Proposal{Expected: frontier(t, s), ID: "parent", Children: []string{"leaf"}, Output: output("parent")})
		})
	}
}

func TestPublicationTailSkipsStayOutsidePrefix(t *testing.T) {
	t.Parallel()
	db, s, _, _ := fixture(t)
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "audit", ConversationID: "conversation", AgentID: "agent", ScaffoldKind: "compaction_recorded", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(40, 0), RecordedAt: time.Unix(40, 0), TimeQuality: "observed", Body: "synthetic audit"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "e3", ConversationID: "conversation", PersonID: "person", AgentID: "agent", ScaffoldKind: "provider_item", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(40, 0), TimeQuality: "observed", Body: "synthetic provider content"})
	if err != nil {
		t.Fatal(err)
	}
	p := proposal(t, s, "leaf")
	p.Verbatim = []string{"e2", "e3"}
	p.TailSkips = []Coverage{{EventID: "audit", Reason: "summary_echo"}}
	for _, kind := range []string{"covered tail skip", "missing skip", "empty reason", "invented reason", "ordinary input", "provider item", "duplicate", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			q := p
			q.TailSkips = append([]Coverage(nil), p.TailSkips...)
			switch kind {
			case "covered tail skip":
				q.Cover = append([]Coverage{{EventID: "e1"}}, q.TailSkips...)
				q.TailSkips = nil
			case "missing skip":
				q.TailSkips = nil
			case "empty reason":
				q.TailSkips[0].Reason = ""
			case "invented reason":
				q.TailSkips[0].Reason = "replay"
			case "ordinary input":
				q.TailSkips = []Coverage{{EventID: "e2", Reason: "scaffold"}, q.TailSkips[0]}
				q.Verbatim = []string{"e3"}
			case "provider item":
				q.TailSkips = append(q.TailSkips, Coverage{EventID: "e3", Reason: "scaffold"})
				q.Verbatim = []string{"e2"}
			case "duplicate":
				q.Verbatim = []string{"e2", "audit", "e3"}
			case "foreign":
				q.TailSkips = append(q.TailSkips, Coverage{EventID: "absent", Reason: "scaffold"})
			}
			if n, err := s.Publish(t.Context(), q); !errors.Is(err, ErrCoverage) || n.ID != "" {
				t.Fatalf("invalid tail published: %+v %v", n, err)
			}
			if got := counts(t, db); !reflect.DeepEqual(got, []int{0, 0, 0}) {
				t.Fatalf("partial publication: %v", got)
			}
		})
	}
	leaf := publish(t, s, p)
	if !reflect.DeepEqual(leaf.Cover, []Coverage{{EventID: "e1"}}) || !leaf.EndAt.Equal(time.Unix(10, 0)) {
		t.Fatalf("tail entered summary provenance: %+v", leaf)
	}
	parent := publish(t, s, Proposal{Expected: frontier(t, s), ID: "parent", Children: []string{leaf.ID}, Cover: []Coverage{{EventID: "e2"}, {EventID: "audit", Reason: "summary_echo"}}, Verbatim: []string{"e3"}, Output: output("extended prefix")})
	if !reflect.DeepEqual(parent.Cover, []Coverage{{EventID: "e2"}, {EventID: "audit", Reason: "summary_echo"}}) {
		t.Fatalf("extended coverage=%+v", parent.Cover)
	}
	ids, _, err := s.ExpandEvents(t.Context(), "agent", parent.ID, 0, 10)
	if err != nil || !reflect.DeepEqual(ids, []string{"e1", "e2"}) {
		t.Fatalf("expanded prefix=%v err=%v", ids, err)
	}
	// Recondensing a multi-level root consumes descendants before direct cover.
	publish(t, s, Proposal{Expected: frontier(t, s), ID: "root", Children: []string{parent.ID}, Verbatim: []string{"e3"}, Output: output("condensed prefix")})
}

func TestBuilderRejectsInterleavedSourcesBeforeModelWork(t *testing.T) {
	t.Parallel()
	b, v, db := builderFixture(t)
	addEvent(t, db, "later", "conversation")
	b.Generate = func(context.Context, PassSegment) (Text, error) {
		t.Fatal("model called for a non-prefix segment")
		return Text{}, nil
	}
	segment := PassSegment{Records: append(v.History, Record{ID: "later"}), Protected: []Record{v.Input}}
	if _, err := b.Summarize(t.Context(), segment); !errors.Is(err, ErrCoverage) {
		t.Fatal(err)
	}
	if got := counts(t, db); !reflect.DeepEqual(got, []int{0, 0, 0}) {
		t.Fatal(got)
	}
}
