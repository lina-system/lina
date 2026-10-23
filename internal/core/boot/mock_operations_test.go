// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestMockOperationsKeepScopeHistoryAndWorkReference(t *testing.T) {
	r, session, _, other := managementFixture(t)
	agent := workAgentFixture(t, r)
	r.agent = agent
	r.conversation = "main:" + agent
	r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{agent: r}}
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	if err = r.startWork(t.Context(), &WorkConfig{Driver: func(*work.Engine) (work.Driver, error) { return parkedWorkDriver{}, nil }}); err != nil {
		t.Fatal(err)
	}
	defer r.workEngine.close(context.Background())
	store, err := r.resultStore(ledger.Surface{Kind: "app"})
	if err != nil {
		t.Fatal(err)
	}
	run := "mock-run"
	scope := result.Scope{PersonID: r.person, AgentID: agent, ConversationID: r.conversation, TurnID: "mock-turn", TaskID: &run}
	body := result.Mock{Request: "Show tasks", Checklist: []result.MockItem{{ID: "tasks", Description: "Tasks", Action: "none"}}, Page: result.AssetRef{ID: "page", Revision: 1}, PNG: result.AssetRef{ID: "png", Revision: 1}, StopReason: "all_passed"}
	for _, id := range []string{"elements", "controls", "tokens", "network", "errors", "layout_390", "layout_1280"} {
		body.Checks = append(body.Checks, result.MockCheck{ID: id, Passed: true})
	}
	raw, _ := json.Marshal(body)
	p := result.PresentRequest{Type: result.MockType, Title: "Mock", Summary: "Checked", Body: raw}
	first, err := store.PresentMock(t.Context(), scope, p, run)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.MockRequest{Action: "read", ResultID: first.ID, Revision: "1", ConversationID: r.conversation}
	read := func(s clientsrv.Session, p protocol.MockRequest, want protocol.ErrorCode) {
		t.Helper()
		got, err := r.mockOperation(t.Context(), s, protocol.Envelope{MessageID: "read", Payload: p})
		if want == "" {
			if err != nil || got.Payload.(protocol.MockResponse).Result == nil {
				t.Fatalf("read: %+v %v", got, err)
			}
			return
		}
		var refusal *clientsrv.OperationError
		if !errors.As(err, &refusal) || refusal.Code != want {
			t.Fatalf("want %s got %v", want, err)
		}
	}
	read(session, request, "")
	read(clientsrv.Session{PersonID: "other"}, request, protocol.ErrorNotAuthorized)
	foreign := request
	foreign.ConversationID = "main:" + other
	read(session, foreign, protocol.ErrorOutOfScope)
	target := "mini_app"
	repos := []string{"example/project"}
	key := "build-example"
	build := request
	build.Action = "build"
	build.Target = &target
	build.Repositories = &repos
	for range 2 {
		got, err := r.mockOperation(t.Context(), session, protocol.Envelope{MessageID: "build", IdempotencyKey: &key, Payload: build})
		if err != nil || got.Payload.(protocol.MockResponse).GoalID == nil {
			t.Fatalf("handoff: %+v %v", got, err)
		}
	}
	snapshot, err := r.workEngine.engine.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Goals) != 1 || !strings.Contains(strings.Join(snapshot.Goals[0].Assumptions, "\n"), `"asset_id":"page"`) || snapshot.Goals[0].Criteria[0].Statement != "Tasks" {
		t.Fatalf("work lost pinned mock/checklist: %+v", snapshot.Goals)
	}
	run = "redraw"
	p.ID = first.ID
	p.BaseRevision = 1
	body.Previous = &result.ResultRef{ID: first.ID, Revision: 1}
	p.Body, _ = json.Marshal(body)
	if _, err = store.PresentMock(t.Context(), scope, p, run); err != nil {
		t.Fatal(err)
	}
	read(session, request, "")                    // old revision stays readable
	read(session, build, protocol.ErrorMalformed) // build requires the stable native key
	_, err = r.mockOperation(t.Context(), session, protocol.Envelope{MessageID: "stale", IdempotencyKey: &key, Payload: build})
	var refusal *clientsrv.OperationError
	if !errors.As(err, &refusal) || refusal.Code != protocol.ErrorStaleRevision {
		t.Fatalf("stale handoff: %v", err)
	}
}
