// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestBuilderCompactsPersistedNonPrefixRoot(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "leaf"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			db, s, _, cfg := fixture(t)
			text := Text{Requests: "continue", Topics: "facts", Actions: "none", Values: "e1", Omitted: "wording"}
			raw, _ := json.Marshal(text)
			p := proposal(t, s, "legacy-leaf")
			p.Output = output(string(raw))
			p.Output.BuiltAt = p.Output.BuiltAt.UTC()
			root := publish(t, s, p)
			if nested {
				root = publish(t, s, Proposal{ID: "legacy-root", Expected: frontier(t, s), Children: []string{root.ID}, Verbatim: []string{"e2"}, Output: p.Output})
			}
			_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "audit", ConversationID: "conversation", AgentID: "agent", ScaffoldKind: "compaction_recorded", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(40, 0), RecordedAt: time.Unix(40, 0), TimeQuality: "recorded", Body: "synthetic audit"})
			if err != nil {
				t.Fatal(err)
			}
			addEvent(t, db, "e3", "conversation")
			// The old builder owned all exclusions, even past the retained e2.
			// Seed its on-disk representation without using today's publisher.
			if err = db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), `INSERT INTO sum_cover(node_id,event_id,ordinal,reason) VALUES('legacy-leaf','audit',1,'summary_echo')`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			db, err = canon.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close(context.Background()) })
			s = New(db)
			b := &Builder{Store: s, Events: ledger.New(db, nil), Agent: "agent", Conversation: "conversation", Model: "model", Now: func() time.Time { return time.Unix(50, 0) }}
			b.NextID = func() string { return "interleaved" }
			b.Generate = func(context.Context, PassSegment) (Text, error) {
				t.Fatal("legacy recovery admitted interleaved new input")
				return Text{}, nil
			}
			before := counts(t, db)
			if _, err := b.Summarize(t.Context(), PassSegment{RootID: root.ID, Records: []Record{{ID: "e3"}}, Protected: []Record{{ID: "e2"}}}); !errors.Is(err, ErrCoverage) {
				t.Fatalf("interleaved new input: %v", err)
			}
			if !reflect.DeepEqual(counts(t, db), before) {
				t.Fatal("rejected compaction changed stored coverage")
			}
			for _, extend := range []bool{false, true} {
				id := "condensed"
				segment := PassSegment{RootID: root.ID, Protected: []Record{{ID: "e2"}, {ID: "e3"}}}
				var wantInputs []string
				if extend {
					id = "extended"
					segment.Records = []Record{{ID: "e2"}}
					segment.Protected = []Record{{ID: "e3"}}
					wantInputs = []string{"e2"}
				}
				b.NextID = func() string { return id }
				b.Generate = func(_ context.Context, got PassSegment) (Text, error) {
					var inputs []string
					for _, record := range got.Records {
						inputs = append(inputs, record.ID)
					}
					if got.RootID != root.ID || !reflect.DeepEqual(inputs, wantInputs) {
						t.Fatalf("summary sources: root=%q inputs=%v", got.RootID, inputs)
					}
					return text, nil
				}
				summary, err := b.Summarize(t.Context(), segment)
				if err != nil {
					t.Fatalf("compact persisted root (%s): %v", id, err)
				}
				published, err := b.Publish(t.Context(), segment, summary)
				if err != nil {
					t.Fatalf("publish recovered root (%s): %v", id, err)
				}
				previous := root.ID
				root, err = s.Node(t.Context(), published.ID)
				if err != nil || !reflect.DeepEqual(root.Children, []string{previous}) {
					t.Fatalf("lineage=%v err=%v", root.Children, err)
				}
				if !extend && !root.EndAt.Equal(time.Unix(10, 0)) {
					t.Fatalf("legacy tail exclusion extended new summary bounds: %v", root.EndAt)
				}
				wantEvents := []string{"e1"}
				if extend {
					wantEvents = append(wantEvents, "e2")
				}
				ids, _, err := s.ExpandEvents(t.Context(), "agent", root.ID, 0, 10)
				if err != nil || !reflect.DeepEqual(ids, wantEvents) {
					t.Fatalf("expanded=%v want=%v err=%v", ids, wantEvents, err)
				}
			}
			for _, id := range []string{"e1", "e2", "audit", "e3", "compaction:condensed", "compaction:extended"} {
				if _, err := b.Events.Get(t.Context(), id); err != nil {
					t.Fatalf("missing original or publication event %q: %v", id, err)
				}
			}
			legacy, err := s.Node(t.Context(), "legacy-leaf")
			if err != nil || legacy.Output != p.Output || !reflect.DeepEqual(legacy.Cover, []Coverage{{EventID: "e1"}, {EventID: "audit", Reason: "summary_echo"}}) {
				t.Fatalf("legacy evidence changed: %+v err=%v", legacy, err)
			}
		})
	}
}

func TestPublicationRejectsInvalidPersistedCoverage(t *testing.T) {
	for _, kind := range []string{"input gap", "invented exclusion", "provider content exclusion"} {
		t.Run(kind, func(t *testing.T) {
			db, s, _, _ := fixture(t)
			publish(t, s, proposal(t, s, "legacy"))
			in := ledger.Input{ID: "e3", ConversationID: "conversation", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(40, 0), RecordedAt: time.Unix(40, 0), TimeQuality: "recorded", Body: "synthetic content"}
			if kind == "provider content exclusion" {
				in.ScaffoldKind = "provider_item"
			}
			if _, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, in); err != nil {
				t.Fatal(err)
			}
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				reason := ""
				if kind != "input gap" {
					reason = "scaffold"
				}
				_, err := tx.ExecContext(t.Context(), `INSERT INTO sum_cover(node_id,event_id,ordinal,reason) VALUES('legacy','e3',1,?)`, reason)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := counts(t, db)
			p := Proposal{ID: "candidate", Expected: frontier(t, s), Children: []string{"legacy"}, Cover: []Coverage{{EventID: "e2"}}, Output: output("candidate")}
			if n, err := s.Publish(t.Context(), p); !errors.Is(err, ErrCoverage) || n.ID != "" {
				t.Fatalf("invalid stored coverage accepted: %+v %v", n, err)
			}
			if !reflect.DeepEqual(counts(t, db), before) {
				t.Fatal("failed recovery changed stored coverage")
			}
		})
	}
}
