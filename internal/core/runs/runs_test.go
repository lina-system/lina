// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/materials"
	"github.com/thisisjun786/lina/kit/provider"
)

func setup(t *testing.T, stream loop.StreamFunc) (*runs.Service, *canon.Store, *assets.Store) {
	return setupWith(t, stream, nil)
}
func setupWith(t *testing.T, stream loop.StreamFunc, configure func(*runs.Config), schemas ...string) (*runs.Service, *canon.Store, *assets.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: runs.Schema})
	for _, schema := range schemas {
		migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: schema})
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := runs.Config{Epoch: 1, WorkDir: filepath.Join(root, "work"), DB: db, Assets: store, Stream: stream, Authorize: func(context.Context, runs.Origin, runs.Brief) error { return nil }, Tools: map[string]runs.Tool{
		"read": {Declaration: json.RawMessage(`{"type":"function","name":"read"}`), Kind: runs.Read, Authorize: func(context.Context, runs.Record, provider.ToolCall) error { return nil }, Handler: loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
			return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`"evidence"`)}
		}}},
		"shell": {Declaration: json.RawMessage(`{"type":"function","name":"shell"}`)},
	}}
	// Unit scenarios inject publication; full turn scenarios replace this with
	// turn.PublishPromotion and verify both records in the shared transaction.
	cfg.PublishPromotion = func(context.Context, *canon.Tx, string, budget.Handle, budget.Handoff) error { return nil }
	if configure != nil {
		configure(&cfg)
	}
	service, err := runs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	return service, db, store
}
func brief() runs.Brief {
	return runs.Brief{Goal: "Read the named evidence", Scope: "named materials", NotTouch: "everything else", Done: "report the evidence", Result: "document_draft", Allow: []string{"read", "shell"}, Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
}
func origin() runs.Origin {
	return runs.Origin{Person: "person-test", Agent: "agent-test", Conversation: "conversation-test", Turn: "turn-test"}
}
func start(t *testing.T, s *runs.Service) string {
	t.Helper()
	id, state, err := s.Start(t.Context(), origin(), brief())
	if err != nil || id == "" || state != runs.Working {
		t.Fatalf("start=%s %s %v", id, state, err)
	}
	return id
}

type stream struct{ events []provider.Event }

func (s *stream) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*stream) Meta() provider.Meta { return provider.Meta{} }
func (*stream) Close() error        { return nil }
func answer(a runs.Artifact) provider.Stream {
	b, _ := json.Marshal(a)
	item, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": string(b)}}})
	return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: item}, {Kind: provider.Terminal}}}
}
func TestStartReturnsBeforeFirstCallAndScopeIsNotDeclared(t *testing.T) {
	var calls atomic.Int64
	s, _, _ := setup(t, func(ctx context.Context, r provider.Request) (provider.Stream, error) {
		calls.Add(1)
		if _, ok := ctx.Deadline(); ok {
			t.Error("run has a time cap")
		}
		if len(r.Tools) != 1 || string(r.Tools[0]) != `{"type":"function","name":"read"}` {
			t.Errorf("tools=%s", r.Tools)
		}
		if r.Purpose != provider.Background || len(r.Items) != 1 {
			t.Errorf("request=%+v", r)
		}
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	id, _, err := s.Start(ctx, origin(), brief())
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("provider called before start receipt")
	}
	// The tool caller releases execution only after consuming the committed id.
	if err = s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(t.Context(), id)
	if err != nil || r.Outcome != runs.Complete || calls.Load() != 1 {
		t.Fatalf("record=%+v calls=%d error=%v", r, calls.Load(), err)
	}
}
func TestSecondInterruptionLandsAndCancelledNeverResumes(t *testing.T) {
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return nil, errors.New("interrupted")
	})
	id := start(t, s)
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get(t.Context(), id)
	if r.Retries != 1 || r.State != runs.Waiting {
		t.Fatalf("first recovery=%+v", r)
	}
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(t.Context(), id)
	if r.Outcome != runs.Failed {
		t.Fatalf("second interruption=%+v", r)
	}
	stopped := start(t, s)
	if err := s.Stop(t.Context(), stopped); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(t.Context(), stopped)
	if r.Outcome != runs.Cancelled {
		t.Fatalf("cancelled=%+v", r)
	}
}
func TestUnresolvedCitationLandsPartialMaterial(t *testing.T) {
	s, _, store := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		return answer(runs.Artifact{Body: "cited report", Done: true, Citations: []materials.Anchor{{AssetID: "missing", Revision: 1, Start: 0, End: 1, Hash: "missing"}}}), nil
	})
	id := start(t, s)
	if err := s.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(t.Context(), id)
	if err != nil || r.Outcome != runs.Partial || len(r.Citations) != 1 || r.Citations[0].Resolved {
		t.Fatalf("record=%+v error=%v", r, err)
	}
	data, err := store.Read(t.Context(), r.Material.ID, r.Material.Revision)
	if err != nil || !json.Valid(data) {
		t.Fatalf("material=%s error=%v", data, err)
	}
	notices, err := s.Notices(t.Context(), origin().Conversation)
	if err != nil || len(notices) != 1 || notices[0].Outcome != runs.Partial {
		t.Fatalf("notices=%+v error=%v", notices, err)
	}
}

func TestStartToolReturnsDurableReceiptBeforeExecution(t *testing.T) {
	var calls atomic.Int64
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		calls.Add(1)
		return answer(runs.Artifact{Body: "report", Done: true}), nil
	})
	b, _ := json.Marshal(brief())
	tool := s.StartTool(origin())
	c := provider.ToolCall{ID: "start-call", Name: runs.StartToolName, Arguments: string(b)}
	if _, err := tool.Validate(c); err != nil {
		t.Fatal(err)
	}
	result := tool.Execute(t.Context(), c)
	var receipt runs.Receipt
	if err := json.Unmarshal(result.Output, &receipt); err != nil || result.Code != loop.OK || receipt.ID == "" || calls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d error=%v", result, calls.Load(), err)
	}
	if _, err := s.Get(t.Context(), receipt.ID); err != nil {
		t.Fatal(err)
	}
	release(t, s, receipt.ID)
	if r := wait(t, s, receipt.ID); r.Outcome != runs.Complete {
		t.Fatalf("record=%+v", r)
	}
	c.Arguments = `{"Person":"different-person"}`
	if _, err := tool.Validate(c); err == nil {
		t.Fatal("accepted identity from model arguments")
	}
}
