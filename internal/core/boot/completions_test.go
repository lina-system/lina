// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/work"
	"testing"
	"time"
)

func TestCompletionStartupRequiresInitializedRuntime(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	defer r.lanes.Close(context.Background())
	r.work = work.New(r.db)
	r.route = &modelpath.ResponsesConfig{}
	err := r.startCompletions(t.Context())
	if r.completionStop != nil {
		r.completionStop()
		<-r.completionDone
	}
	if err == nil {
		t.Fatal("completion delivery started before background service and final lanes were initialized")
	}
}

func TestCompletionInputIncludedInModelContext(t *testing.T) {
	r := controlRuntime(t)
	first, err := r.agents.FirstRun(t.Context(), r.person, true)
	if err != nil {
		t.Fatal(err)
	}
	view := newConversationView(r, first.Agent.ID, "main:"+first.Agent.ID)
	now := time.Now()
	event, err := view.journal().Admit(t.Context(), guard.EntrySystem, ledger.Input{ID: "completion-input", ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "manager"}, ScaffoldKind: "work_completion", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: `{"receipt":"saved"}`})
	if err != nil {
		t.Fatal(err)
	}
	items, _, _, events, err := view.modelContext(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(events) != 1 || events[0].ID != event.ID {
		t.Fatalf("completion omitted from manager context: items=%d events=%v", len(items), events)
	}
}
