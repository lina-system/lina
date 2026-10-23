// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
	"github.com/thisisjun786/lina/internal/core/canon"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type echoStream struct{ events []provider.Event }

func (s *echoStream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (s *echoStream) Close() error        { return nil }
func (s *echoStream) Meta() provider.Meta { return provider.Meta{Class: provider.ModelAPI} }

// The composition follows preparation's public contract: fresh retraction
// snapshot, scoped recall, filtered history/packet, epoch-checked summary and
// a final epoch check before the actual provider loop dispatches.
func observeRequest(t *testing.T, db *canon.Store, s *retract.Store, flow *memoryexec.Memory, id string, summaryEpoch uint64, want bool) {
	t.Helper()
	view, err := s.Snapshot(t.Context(), "person-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	var events []ledger.Event
	event, err := ledger.New(db, nil).Get(t.Context(), "source-1")
	if err == nil && view.Visible(retract.Ref{Kind: retract.Event, ID: event.ID}) {
		events = append(events, event)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	history, err := assemble.History(events, "conversation-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	// A prepared packet from the previous request is untrusted until rechecked.
	cached := assemble.ContextItem{ID: id, Kind: assemble.Memory, Text: forgotten, Evidence: []assemble.Ref{{ID: "source-1"}}}
	packet, _, err := atropos.Packet(t.Context(), nil, 0, func(context.Context) (assemble.ContextPacket, error) {
		if !view.Visible(retract.Ref{Kind: retract.Memory, ID: cached.ID}) {
			return nil, nil
		}
		for _, ref := range cached.Evidence {
			if !view.Visible(retract.Ref{Kind: retract.Event, ID: ref.ID}) {
				return nil, nil
			}
		}
		return assemble.ContextPacket{cached}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var summary provider.Item
	if view.DerivedValid(summaryEpoch, []retract.Ref{{Kind: retract.Event, ID: "source-1"}}) {
		summary, _ = json.Marshal(map[string]string{"role": "developer", "content": forgotten})
	}
	request, report := assemble.Assemble(assemble.Input{Core: "synthetic rules", AgentID: "agent-1", PersonaRevision: "1", Model: "synthetic", Summary: summary, History: history, Packet: packet, Current: json.RawMessage(`{"role":"user","content":"What do you recall?"}`), MaxTokens: 1000}, func(r provider.Request) int { return len(r.Items) })
	if report.Failure != "" {
		t.Fatal(report)
	}
	var answer provider.Item
	_, err = loop.Run(t.Context(), loop.Config{Request: request, Stream: func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		if err := s.Validate(ctx, view.Epoch); err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(r.Items)
		answer, _ = json.Marshal(map[string]string{"role": "assistant", "content": string(raw)})
		return &echoStream{events: []provider.Event{{Kind: provider.ItemDone, Item: answer}, {Kind: provider.Terminal}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rawPacket, _ := json.Marshal(packet)
	rawRequest, _ := json.Marshal(request.Items)
	for name, data := range map[string][]byte{"summary": summary, "packet": rawPacket, "request": rawRequest, "answer": answer} {
		if bytes.Contains(data, []byte(forgotten)) != want {
			t.Fatalf("%s exposure=%v want=%v", name, bytes.Contains(data, []byte(forgotten)), want)
		}
	}
	recalled, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "agent-1", Person: "person-1"}, memoryexec.PersonRecall{Question: "What preference?", Query: "violet", Depth: 1, At: 100})
	if err != nil {
		t.Fatal(err)
	}
	if (len(recalled.Sources) > 0) != want || (len(recalled.Answer) > 0) != want {
		t.Fatalf("recall answer exposure: %+v want=%v", recalled, want)
	}
}

func snapshotCanon(t *testing.T, source, target string) {
	t.Helper()
	reader, err := canon.OpenSQLiteReader(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := canon.BackupSQLite(t.Context(), reader, target); err != nil {
		t.Fatal(err)
	}
}

func TestNextRequestAndOlderGenerationHaveZeroExposure(t *testing.T) {
	t.Parallel()
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "trash", true: "tombstone"}[permanent], func(t *testing.T) {
			t.Parallel()
			db, s, flow, root, close := memoryFixture(t)
			receipt := seedMemory(t, db, flow, "source-1", "conversation-1", forgotten)
			epoch, err := s.Epoch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			observeRequest(t, db, s, flow, receipt.ID, epoch, true)
			generation := filepath.Join(t.TempDir(), "canon.sqlite")
			snapshotCanon(t, filepath.Join(root, "canon.sqlite"), generation)
			r := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}
			if err := s.Forget(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			if permanent {
				if err := s.Delete(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			observeRequest(t, db, s, flow, receipt.ID, epoch, false)
			removals, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
			if err != nil {
				t.Fatal(err)
			}
			close()
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "backups"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "backups", "removals"), removals, 0600); err != nil {
				t.Fatal(err)
			}
			snapshotCanon(t, generation, filepath.Join(root, "canon.sqlite"))
			db, s, flow, _, _ = memoryAt(t, root)
			observeRequest(t, db, s, flow, receipt.ID, epoch, false)
			if receipt, err := flow.Remember(t.Context(), memoryWrite("source-1", forgotten)); err == nil || receipt.Remembered() {
				t.Fatal("restore enabled re-extraction")
			}
		})
	}
}
