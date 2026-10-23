// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Exercise actual ledger commits and inspect both durable records and raw SQLite
// bytes after restart: the packet is sent, while only its references are stored.
func TestAssemblyThroughDurableTurnNeverStoresPacket(t *testing.T) {
	db, config := database(t)
	s := turn.New(db)
	const packetText = "ephemeral-context-body-should-never-be-stored"
	events := ledger.New(db, nil)
	writer := turn.EvidenceWriter{Ledger: events, ConversationID: "conversation", Surface: ledger.Surface{Kind: "tui"}, Now: time.Now}
	var first provider.Request
	for _, id := range []string{"first", "next"} {
		p := preparation()
		p.Digest = assemble.Digest
		p.Acknowledge = writer.Acknowledge
		p.RecordRequest = writer.RecordRequest
		p.Context = func(ctx context.Context, m turn.ModelPolicy) (turn.PreparedRequest, error) {
			packet, _, err := atropos.Packet(ctx, assemble.ContextPacket{{ID: "clock", Kind: assemble.Clock, Text: packetText}}, 0, nil)
			if err != nil {
				return turn.PreparedRequest{}, err
			}
			if id == "next" {
				packet[0].Text = "fresh context"
			}
			r, report := assemble.Assemble(assemble.Input{Core: "rules", AgentID: "agent", PersonaRevision: "r1", Persona: agents.Layers{Core: "persona"}, Model: m.Model, Effort: m.Effort, Packet: packet, Current: json.RawMessage(`{"role":"user","content":"input"}`), MaxTokens: 100}, func(r provider.Request) int { return len(r.Items) })
			if report.Failure != "" {
				t.Fatal(report)
			}
			a := turn.Assembly{AgentID: report.AgentID, PersonaRevision: report.PersonaRevision, WorkMethodRevision: "method-" + id}
			for _, ref := range report.Items {
				a.Items = append(a.Items, turn.Reference{ID: ref.ID, Revision: ref.Revision})
			}
			return turn.PreparedRequest{Request: r, Assembly: a, MaxTokens: 100, Counter: func(r provider.Request) int { return len(r.Items) }}, nil
		}
		input, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: id}, Body: []byte("input")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Run(t.Context(), turn.Options{ID: id, Conversation: "conversation", InboxID: input.ID, Preparation: p, Loop: loop.Config{Stream: func(_ context.Context, r provider.Request) (provider.Stream, error) {
			record, err := events.Get(t.Context(), "request:"+id+":0")
			if err != nil {
				t.Fatal("provider called before ledger commit", err)
			}
			var evidence turn.RequestEvidence
			if err := json.Unmarshal([]byte(record.Body.Text()), &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.Hash != assemble.Digest(r) || evidence.Class != provider.ModelAPI || evidence.Assembly.Items[0].ID != "clock" || evidence.Assembly.WorkMethodRevision != "method-"+id {
				t.Fatal(evidence)
			}
			if id == "first" {
				first = r
			} else {
				if len(first.Items) != len(r.Items) {
					t.Fatal("old packet grew history")
				}
				for i := 0; i < 6; i++ {
					if !bytes.Equal(first.Items[i], r.Items[i]) {
						t.Fatal("fixed prefix changed")
					}
				}
				raw, _ := json.Marshal(r.Items)
				if bytes.Contains(raw, []byte(packetText)) {
					t.Fatal("previous packet returned in next turn")
				}
			}
			return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: json.RawMessage(`{"role":"assistant","content":"answer"}`)}, {Kind: provider.Terminal}}}, nil
		}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	config.Epoch++
	db = open(t, config)
	s = turn.New(db)
	events = ledger.New(db, nil)
	for _, id := range []string{"first", "next"} {
		ack, err := events.Get(t.Context(), "prepare:"+id)
		if err != nil {
			t.Fatal(err)
		}
		var a turn.Acknowledgement
		if err := json.Unmarshal([]byte(ack.Body.Text()), &a); err != nil {
			t.Fatal(err)
		}
		if a.PolicyRevision != "policy-1" || a.Assembly.AgentID != "agent" || a.Assembly.PersonaRevision != "r1" || a.Assembly.WorkMethodRevision != "method-"+id {
			t.Fatal(a)
		}
		row, err := s.Read(t.Context(), id)
		if err != nil || !row.Prepared || len(row.Items) != 1 {
			t.Fatalf("%+v %v", row, err)
		}
	}
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM conv_event").Scan(&n)
	}); err != nil || n != 4 {
		t.Fatalf("evidence=%d %v", n, err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{config.Path, config.Path + "-wal"} {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(packetText)) {
			t.Fatal("packet persisted in SQLite bytes")
		}
	}
}
