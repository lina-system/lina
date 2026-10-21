// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type leaser struct{}

func (leaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}
func database(t *testing.T) (*canon.Store, canon.Config) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: canon.InboxSchema + turn.Schema})
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: c, Migrations: ms}
	return open(t, cfg), cfg
}
func open(t *testing.T, c canon.Config) *canon.Store {
	t.Helper()
	s, err := canon.Open(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}
func admit(t *testing.T, s *canon.Store) int64 {
	t.Helper()
	r, err := inbox.New(s).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "client", NativeID: "message"}, Body: []byte("input")})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}
func TestClaimPreparationAndRecoveryNeverReplay(t *testing.T) {
	db, cfg := database(t)
	s := turn.New(db)
	id := admit(t, db)
	if err := s.Claim(t.Context(), "turn", "conversation", id); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepared(t.Context(), "turn"); err != nil {
		t.Fatal(err)
	}
	j := s.Journal("turn")
	raw := json.RawMessage(`{ "type":"function_call", "call_id":"a", "name":"edit", "arguments":"{}" }`)
	if err := j.Item(t.Context(), 0, 0, raw); err != nil {
		t.Fatal(err)
	}
	calls := []provider.ToolCall{{ID: "a", Name: "edit", Arguments: `{}`}, {ID: "b", Name: "edit", Arguments: `{}`}}
	if err := j.Calls(t.Context(), 0, calls); err != nil {
		t.Fatal(err)
	}
	if err := j.Result(t.Context(), 0, 0, loop.Result{ID: "a", Code: loop.OK, Output: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = open(t, cfg)
	s = turn.New(db)
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Claim(t.Context(), "new-turn", "conversation", id); !errors.Is(err, turn.ErrAccepted) {
		t.Fatal(err)
	}
	row, err := s.Read(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	if row.State != turn.Aborted || !row.Prepared || len(row.Results) != 2 || row.Results[0].Code != loop.OK || row.Results[1].Code != loop.Aborted || string(row.Items[0]) != string(raw) {
		t.Fatalf("%+v", row)
	}
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestEffectKeyScopeAndIntent(t *testing.T) {
	a := turn.EffectKey("turn", "edit", "target", "call")
	for _, b := range []string{turn.EffectKey("task", "edit", "target", "call"), turn.EffectKey("turn", "edit", "target", "other"), turn.EffectKey("turn", "edit", "else", "call"), turn.EffectKey("turn", "read", "target", "call")} {
		if a == b || a == "" {
			t.Fatal("key collision")
		}
	}
}
func TestEffectAppliedOnceAcrossRestart(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash_before_receipt", true: "receipt_committed"}[receipt], func(t *testing.T) {
			db, cfg := database(t)
			runs := 0
			auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: cfg.Epoch, TaskID: "turn", Host: "host", OSUser: "user", Person: "person", Agent: "agent", Revision: "r1"}
			spec := tools.Spec{Name: "edit", Conc: 1, Deadline: time.Second, Placement: tools.Core, Schema: func(raw json.RawMessage) (tools.Input, error) {
				return tools.Input{Arguments: raw, Target: "document", What: "edit", Where: "document", ScopeKey: "edit", Write: true, Undoable: true}, nil
			}}
			makeRegistry := func() (*authority.Service, *tools.Registry) {
				svc, err := authority.Open(t.Context(), db, authority.Config{Epoch: cfg.Epoch, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
					return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "edit", Target: "document", Write: true}}, Revisions: map[string]string{"document": "r1"}}, grant.Environment{Epoch: cfg.Epoch, Now: time.Now(), OSPermitted: true, Revision: "r1"}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				reg := tools.NewRegistry(svc, leaser{})
				if err = reg.Register(spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
					runs++
					return tools.Result{State: tools.Applied, Revision: "r2", Body: json.RawMessage(`{}`)}, nil
				}); err != nil {
					t.Fatal(err)
				}
				return svc, reg
			}
			svc, reg := makeRegistry()
			c := provider.ToolCall{ID: "call", Name: "edit", Arguments: `{}`}
			tool := turn.Tool(reg, spec, tools.Call{Auth: auth}, "turn")
			if !receipt {
				in, _ := spec.Schema(json.RawMessage(c.Arguments))
				_, err := svc.Begin(t.Context(), tools.Call{Key: turn.EffectKey("turn", "edit", "document", "call"), Name: "edit", Auth: auth, Args: in.Arguments}, spec, in)
				if err != nil {
					t.Fatal(err)
				}
				runs++ // effect happened; process died before Finish
			} else {
				if r := tool.Execute(t.Context(), c); r.Code != loop.OK {
					t.Fatal(r)
				}
			}
			if err := db.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			cfg.Epoch++
			auth.Epoch = cfg.Epoch
			db = open(t, cfg)
			_, reg = makeRegistry()
			tool = turn.Tool(reg, spec, tools.Call{Auth: auth}, "turn")
			result := tool.Execute(t.Context(), c)
			want := loop.Unknown
			if receipt {
				want = loop.OK
			}
			if runs != 1 || result.Code != want {
				t.Fatalf("runs=%d result=%+v", runs, result)
			}
		})
	}
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
func TestRunClaimsBeforePreparationAndNeverResends(t *testing.T) {
	db, _ := database(t)
	s := turn.New(db)
	id := admit(t, db)
	requests := 0
	prepared := false
	cfg := loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		row, err := s.Read(t.Context(), "turn")
		if err != nil || !row.Prepared || !prepared {
			t.Errorf("request before preparation: %+v %v", row, err)
		}
		return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
	}}
	opts := turn.Options{ID: "turn", Conversation: "conversation", InboxID: id, Loop: cfg, Prepare: func(context.Context) error { prepared = true; return nil }}
	if _, err := s.Run(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(t.Context(), opts); !errors.Is(err, turn.ErrAccepted) {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatal(requests)
	}
}

func TestRecoveryClosesCallsCommittedAsItemsBeforeResponseEnds(t *testing.T) {
	db, cfg := database(t)
	s := turn.New(db)
	id := admit(t, db)
	if err := s.Claim(t.Context(), "turn", "conversation", id); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"type":"function_call","call_id":"a","name":"edit","arguments":"{}"}`)
	if err := s.Journal("turn").Item(t.Context(), 0, 2, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = open(t, cfg)
	s = turn.New(db)
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := s.Read(t.Context(), "turn")
	if err != nil || len(row.Results) != 1 || row.Results[0].Code != loop.Aborted {
		t.Fatalf("%+v %v", row, err)
	}
}
func TestClaimRollbackAndWrongConversation(t *testing.T) {
	db, _ := database(t)
	s := turn.New(db)
	id := admit(t, db)
	if err := s.Claim(t.Context(), "turn", "wrong", id); !errors.Is(err, turn.ErrInvalid) {
		t.Fatal(err)
	}
	if err := s.Claim(t.Context(), "turn", "conversation", id); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalResumesStoredArgumentsWithFreshDeadline(t *testing.T) {
	db, cfg := database(t)
	auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: cfg.Epoch, TaskID: "turn", Host: "host", OSUser: "user", Person: "person", Agent: "agent"}
	svc, err := authority.Open(t.Context(), db, authority.Config{Epoch: cfg.Epoch, CanPresent: func() bool { return true }, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "notify", Target: "destination"}}}, grant.Environment{Epoch: cfg.Epoch, Now: time.Now(), OSPermitted: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	spec := tools.Spec{Name: "notify", Plugin: "plugin", Outward: true, Conc: 1, Deadline: time.Second, Placement: tools.Core, Schema: func(raw json.RawMessage) (tools.Input, error) {
		return tools.Input{Arguments: raw, Target: "destination", What: "notify", Where: "destination", ScopeKey: "notify"}, nil
	}}
	reg := tools.NewRegistry(svc, leaser{})
	runs := 0
	var received string
	if err = reg.Register(spec, func(_ context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		runs++
		received = string(in.Arguments)
		return tools.Result{State: tools.Applied}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := turn.Tool(reg, spec, tools.Call{Auth: auth}, "turn").Execute(ctx, provider.ToolCall{ID: "a", Name: "notify", Arguments: `{"text":"original"}`})
	cancel()
	if result.Code != loop.AwaitingPerson || runs != 0 {
		t.Fatalf("%+v runs=%d", result, runs)
	}
	pending, err := svc.Pending(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("%+v %v", pending, err)
	}
	if err = svc.Answer(t.Context(), pending[0].ID, authority.Decider{Person: "person", Epoch: cfg.Epoch, Surface: authority.App, Authenticated: true}, authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	resumed, err := turn.ResumeApproval(t.Context(), svc, reg, pending[0].ID, tools.Call{Auth: auth, Args: json.RawMessage(`{"text":"tampered"}`)})
	if err != nil || resumed.State != tools.Applied || runs != 1 || received != `{"text":"original"}` {
		t.Fatalf("%+v %v runs=%d received=%s", resumed, err, runs, received)
	}
}
func TestResultCommitsOnCompletionBeforeEarlierCallFinishes(t *testing.T) {
	db, _ := database(t)
	s := turn.New(db)
	id := admit(t, db)
	if err := s.Claim(t.Context(), "turn", "conversation", id); err != nil {
		t.Fatal(err)
	}
	j := s.Journal("turn")
	secondCommitted := make(chan struct{})
	save := j.Result
	j.Result = func(ctx context.Context, round, index int, r loop.Result) error {
		err := save(ctx, round, index, r)
		if index == 1 {
			close(secondCommitted)
		}
		return err
	}
	a := provider.ToolCall{ID: "a", Name: "read", Arguments: `{}`}
	b := a
	b.ID = "b"
	requests := 0
	cfg := loop.Config{Journal: j, Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"read"}`)}}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests > 1 {
			return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
		}
		var es []provider.Event
		for i, c := range []provider.ToolCall{a, b} {
			raw, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments})
			es = append(es, provider.Event{Kind: provider.ItemDone, Index: int64(i), Item: raw}, provider.Event{Kind: provider.ToolCallReady, Index: int64(i), Call: &c})
		}
		return &stream{append(es, provider.Event{Kind: provider.Terminal})}, nil
	}}
	cfg.Tools = map[string]loop.Tool{"read": {Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		if c.ID == "a" {
			select {
			case <-secondCommitted:
			case <-ctx.Done():
			}
			row, err := s.Read(ctx, "turn")
			if err != nil || len(row.Results) != 1 || row.Results[0].ID != "b" {
				t.Errorf("early receipt: %+v %v", row, err)
			}
		}
		return loop.Result{ID: c.ID, Code: loop.OK}
	}}}
	if _, err := loop.Run(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	row, err := s.Read(t.Context(), "turn")
	if err != nil || len(row.Results) != 2 || row.Results[0].ID != "a" || row.Results[1].ID != "b" {
		t.Fatalf("%+v %v", row, err)
	}
}
