// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestAddOperations(t *testing.T) {
	var identity clientsrv.Identity
	called := false
	original := clientsrv.Operation{Capability: clientsrv.ConversationCapability, Disconnect: func(clientsrv.Session) { called = true }}
	if err := addOperations(&identity, map[protocol.Kind]clientsrv.Operation{protocol.KindInput: original}); err != nil {
		t.Fatal(err)
	}
	alias := identity.Operations
	if err := addOperations(&identity, map[protocol.Kind]clientsrv.Operation{protocol.KindHistoryGet: original}); err != nil {
		t.Fatal(err)
	}
	if len(alias) != 2 {
		t.Fatal("registration replaced the existing map")
	}
	for _, groups := range [][]map[protocol.Kind]clientsrv.Operation{
		{{protocol.KindInterrupt: original, protocol.KindInput: {}}},
		{{protocol.KindInterrupt: original}, {protocol.KindInterrupt: {}}},
	} {
		if err := addOperations(&identity, groups...); err == nil {
			t.Fatal("duplicate accepted")
		}
		if len(identity.Operations) != 2 {
			t.Fatal("failed batch partially registered")
		}
	}
	identity.Operations[protocol.KindInput].Disconnect(clientsrv.Session{})
	if !called {
		t.Fatal("duplicate replaced original")
	}
	if err := addOperations(&identity, map[protocol.Kind]clientsrv.Operation{protocol.KindInput: original}); err == nil || !strings.Contains(err.Error(), string(protocol.KindInput)) {
		t.Fatalf("duplicate kind missing from error: %v", err)
	}
}

func TestWrapOperation(t *testing.T) {
	r := &conversationRuntime{}
	called := false
	if err := r.wrapOperation(protocol.KindInput, func(op clientsrv.Operation) clientsrv.Operation { called = true; return op }); err == nil || called {
		t.Fatalf("missing operation invoked wrapper: %v", err)
	}
	want := errors.New("handler result")
	if err := addOperations(&r.identity, map[protocol.Kind]clientsrv.Operation{protocol.KindInput: {
		Capability: clientsrv.ConversationCapability,
		Handle: func(context.Context, clientsrv.Session, protocol.Envelope) (clientsrv.Result, error) {
			return clientsrv.Result{}, want
		},
		Disconnect: func(clientsrv.Session) { called = true },
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.wrapOperation(protocol.KindInput, func(op clientsrv.Operation) clientsrv.Operation {
		handle := op.Handle
		op.Handle = func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
			if s.PersonID != "owner" || e.MessageID != "input" {
				t.Error("wrapper changed arguments")
			}
			return handle(ctx, s, e)
		}
		return op
	}); err != nil {
		t.Fatal(err)
	}
	op := r.identity.Operations[protocol.KindInput]
	if op.Capability != clientsrv.ConversationCapability {
		t.Fatal("wrapper lost capability")
	}
	if _, err := op.Handle(t.Context(), clientsrv.Session{PersonID: "owner"}, protocol.Envelope{MessageID: "input"}); !errors.Is(err, want) {
		t.Fatalf("wrapper lost error: %v", err)
	}
	op.Disconnect(clientsrv.Session{})
	if !called {
		t.Fatal("wrapper lost disconnect")
	}
}

func TestChainRefresh(t *testing.T) {
	for _, fail := range []int{-1, 0, 1, 2} {
		r := &conversationRuntime{}
		var calls []int
		want := errors.New("refresh failed")
		for i := range 3 {
			r.chainRefresh(func(ctx context.Context, topic string) error {
				if ctx != t.Context() || topic != "waiting" {
					t.Error("refresh changed arguments")
				}
				calls = append(calls, i)
				if i == fail {
					return want
				}
				return nil
			})
		}
		err := r.identity.RefreshTopic(t.Context(), "waiting")
		if (fail == -1 && err != nil) || (fail != -1 && !errors.Is(err, want)) {
			t.Fatalf("refresh error: %v", err)
		}
		expected := []int{0, 1, 2}
		if fail != -1 {
			expected = expected[:fail+1]
		}
		if !reflect.DeepEqual(calls, expected) {
			t.Fatalf("hooks=%v, want %v", calls, expected)
		}
	}
}

func TestConversationRegistrationRejectsDuplicate(t *testing.T) {
	r := controlRuntime(t)
	called := false
	op := r.identity.Operations[protocol.KindApprovalAnswer]
	op.Disconnect = func(clientsrv.Session) { called = true }
	r.identity.Operations[protocol.KindApprovalAnswer] = op
	if err := r.startDecisions(t.Context()); err == nil {
		t.Fatal("duplicate approval_answer registration succeeded")
	}
	r.identity.Operations[protocol.KindApprovalAnswer].Disconnect(clientsrv.Session{})
	if !called {
		t.Fatal("duplicate replaced the original operation")
	}
}

func TestConversationRegistrationSurface(t *testing.T) {
	for _, routed := range []bool{false, true} {
		name := "without-model"
		if routed {
			name = "with-model"
		}
		t.Run(name, func(t *testing.T) {
			root, err := os.MkdirTemp("", "lina-registration-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			ctl, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ctl.Close() })
			if err = ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			var route *modelpath.ResponsesConfig
			if routed {
				route, err = modelpath.ResponsesFromEnv(func(key string) string {
					return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1/v1", "LINA_RESPONSES_MODEL": "fixture", "LINA_RESPONSES_API_KEY": "fixture"}[key]
				})
				if err != nil {
					t.Fatal(err)
				}
				route.Adapter = &turnProvider{}
			}
			r, err := startConversation(t.Context(), root, "registration", 1, ctl, route, lifecycle.NewRegistry(nil), "owner", nil, nil, "", throwawayState())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})

			// This inventory was observed before the refactor. Do not derive it
			// from the registration helpers: it guards the installed surface.
			want := map[protocol.Kind]string{
				protocol.KindAccountRequest: "accounts", protocol.KindSignInListening: "sign_in_relay", protocol.KindSignInRedirect: "sign_in_relay",
				protocol.KindTeamsRequest:    "teams",
				protocol.KindWebexRequest:    "webex",
				protocol.KindTelegramRequest: "telegram_source",
				protocol.KindRecordRequest:   "records", protocol.KindWebRequest: "web", protocol.KindA2aRequest: "a2a",
				protocol.KindExportRequest: "replay", protocol.KindReplayRequest: "replay",
				protocol.KindApprovalAnswer: "waiting", protocol.KindQuestionAnswer: "waiting", protocol.KindWaitingGet: "waiting",
				protocol.KindWorkSwitchRequest: "work", protocol.KindWorkControlRequest: "work",
				protocol.KindWorkLimitRequest: "work",
				protocol.KindProgressGet:      "worker_lanes",
				protocol.KindChannelRequest:   "channels", protocol.KindAgentTalkGet: "exchanges", protocol.KindAgentTalkExpand: "exchanges",
				protocol.KindAgentRequest: "agents", protocol.KindMemoryRequest: "memory",
				protocol.KindPlanRequest: "planning", protocol.KindProgressDigestGet: "progress",
				protocol.KindMiniAppRequest: "mini_apps",
			}
			additional := map[protocol.Kind][]protocol.Capability{
				protocol.KindAgentRequest: {{Name: "growth", Version: "1.0"}},
				protocol.KindProgressGet:  {{Name: "progress", Version: "1.0"}},
			}
			topics := map[string]protocol.Capability{
				"core": {Name: "waiting", Version: "1.0"}, "waiting": {Name: "waiting", Version: "1.0"}, "progress": {Name: "progress", Version: "1.0"},
			}
			if routed {
				want[protocol.KindSurfaceAction] = "results"
				want[protocol.KindMockRequest] = "mocks"

				for _, kind := range []protocol.Kind{protocol.KindConversationStart, protocol.KindHistoryGet, protocol.KindInput, protocol.KindInterrupt, protocol.KindResultGet} {
					want[kind] = "conversation"
				}
				want[protocol.KindLauncherRequest] = "launcher"
				for _, kind := range []protocol.Kind{protocol.KindBindingGet, protocol.KindBindingMove, protocol.KindBindingStatsGet, protocol.KindBindingExport} {
					want[kind] = "conversation"
				}
				for _, kind := range []protocol.Kind{protocol.KindStop, protocol.KindRunningGet, protocol.KindFollowUp, protocol.KindMultiTaskControl} {
					want[kind] = "running"
				}
				additional[protocol.KindHistoryGet] = []protocol.Capability{{Name: "turn_activity", Version: "1.0"}}
				for _, topic := range []string{"core", "conversation:", "results:"} {
					topics[topic] = protocol.Capability{Name: "conversation", Version: "1.0"}
				}
				topics["launcher"] = protocol.Capability{Name: "launcher", Version: "1.0"}
				topics["running"] = protocol.Capability{Name: "running", Version: "1.0"}
			}
			if len(r.identity.Operations) != len(want) {
				t.Errorf("operations=%v, want %v", r.identity.Operations, want)
			}
			for kind, capability := range want {
				op, ok := r.identity.Operations[kind]
				if !ok || op.Handle == nil || op.Capability != (protocol.Capability{Name: capability, Version: "1.0"}) || !reflect.DeepEqual(op.AdditionalCapabilities, additional[kind]) {
					t.Errorf("operation %s: %+v", kind, op)
				}
				if (op.Disconnect != nil) != (kind == protocol.KindAccountRequest || kind == protocol.KindTelegramRequest) {
					t.Errorf("disconnect changed for %s", kind)
				}
				if (op.CapabilityFor != nil) != (kind == protocol.KindAgentRequest || kind == protocol.KindProgressGet) {
					t.Errorf("capability selector changed for %s", kind)
				}
			}
			if !reflect.DeepEqual(r.identity.TopicCapabilities, topics) {
				t.Errorf("topics=%v, want %v", r.identity.TopicCapabilities, topics)
			}
			for _, tc := range []struct {
				kind       protocol.Kind
				payload    protocol.Payload
				capability string
			}{
				{protocol.KindAgentRequest, protocol.AgentRequest{}, "agents"},
				{protocol.KindAgentRequest, protocol.AgentRequest{Operation: str("growth_adopt")}, "growth"},
				{protocol.KindAgentRequest, protocol.AgentRequest{Operation: str("growth_notices")}, "growth"},
				{protocol.KindAgentRequest, protocol.AgentRequest{Operation: str("growth_undo")}, "growth"},
				{protocol.KindProgressGet, protocol.ProgressGet{}, "worker_lanes"},
				{protocol.KindProgressGet, protocol.ProgressGet{View: str("lanes")}, "worker_lanes"},
				{protocol.KindProgressGet, protocol.ProgressGet{View: str("overview")}, "progress"},
			} {
				if got := r.identity.Operations[tc.kind].CapabilityFor(protocol.Envelope{Payload: tc.payload}); got != (protocol.Capability{Name: tc.capability, Version: "1.0"}) {
					t.Errorf("%s selected %+v", tc.kind, got)
				}
			}

			// Join the automatic publishers so only RefreshTopic can publish
			// the new synthetic work below; no sleep or scheduling assumption.
			r.ownerViews.stop()
			<-r.ownerViews.done
			r.waitingStop()
			<-r.waitingDone
			if r.background != nil {
				r.background.stop()
				<-r.background.done
			}
			before := map[string]uint64{}
			for _, topic := range []string{"waiting", "progress", "running"} {
				start, sub, err := r.topics.Subscribe(topic, nil)
				if err != nil {
					t.Fatal(err)
				}
				sub.Close()
				before[topic] = start.Snapshot.Cursor.Seq
			}
			openWorkQuestion(t, r, "registration-work")
			if err := r.work.Start(t.Context(), work.Run{ID: "registration-run", TaskID: "registration-work", Generation: 1, Worker: "Codex", ThreadID: "synthetic-thread", TurnID: "synthetic-turn", NodeID: r.main}); err != nil {
				t.Fatal(err)
			}
			for topic, kind := range map[string]protocol.Kind{"waiting": protocol.KindQuestion, "progress": protocol.KindProgressUpdate, "running": protocol.KindRunningUpdate} {
				if err := r.identity.RefreshTopic(t.Context(), topic); err != nil {
					t.Fatal(err)
				}
				start, sub, err := r.topics.Subscribe(topic, nil)
				if err != nil {
					t.Fatal(err)
				}
				sub.Close()
				found := false
				for _, item := range start.Snapshot.Items {
					if item.Kind == kind {
						found = true
					}
				}
				if want := topic != "running" || routed; found != want {
					t.Errorf("refresh %s published=%v, want %v", topic, found, want)
				}
				if want := topic != "running" || routed; (start.Snapshot.Cursor.Seq > before[topic]) != want {
					t.Errorf("refresh %s cursor=%d -> %d, want changed=%v", topic, before[topic], start.Snapshot.Cursor.Seq, want)
				}
			}
			if err := r.identity.RefreshTopic(t.Context(), "unknown"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDecisionsReplacesRefresh(t *testing.T) {
	r := controlRuntime(t)
	delete(r.identity.Operations, protocol.KindApprovalAnswer)
	delete(r.identity.Operations, protocol.KindQuestionAnswer)
	r.identity.RefreshTopic = func(context.Context, string) error { return errors.New("replaced hook ran") }
	if err := r.startDecisions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.identity.RefreshTopic(t.Context(), "unknown"); err != nil {
		t.Fatal(err)
	}
	if err := r.identity.RefreshTopic(t.Context(), "waiting"); err != nil {
		t.Fatal(err)
	}
}
