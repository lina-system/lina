// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/manifest"
	"github.com/thisisjun786/lina/internal/core/modelpath"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/responses"
)

type scheduledManagerAdapter struct {
	provider.Adapter
	r        *conversationRuntime
	calls    int
	decision string
	err      error
}

func (*scheduledManagerAdapter) Class() provider.Class { return provider.AgentRuntime }
func (a *scheduledManagerAdapter) Stream(ctx context.Context, _ lease.Lease, req provider.Request) (stream provider.Stream, err error) {
	defer func() { a.err = err }()
	a.calls++
	cards, err := a.r.work.Cards(ctx)
	if err != nil || len(cards) != 0 {
		return nil, fmt.Errorf("graph admitted before manager judgment: cards=%d error=%v", len(cards), err)
	}
	if req.Bridge == nil {
		return nil, fmt.Errorf("manager has no decision bridge")
	}
	decision := a.decision
	if decision == "" {
		decision = "delegate"
	}
	args, _ := json.Marshal(map[string]string{"decision": decision, "evidence": "Checked the scoped graph definition."})
	result, err := req.Bridge.Call(ctx, provider.ToolCall{ID: "manager-decision", Name: "coding_decision", Arguments: string(args)})
	if err != nil {
		return nil, err
	}
	var reply loop.Result
	if json.Unmarshal(result.Output, &reply) != nil || reply.Code != loop.OK {
		return nil, fmt.Errorf("manager judgment failed: %s", result.Output)
	}
	return &handoffRuntimeStream{events: []provider.Event{{Kind: provider.Terminal}}}, nil
}

func TestDelegationManagerJudgmentPrecedesDispatch(t *testing.T) {
	r, d, _ := delegationPolicyRuntime(t)
	removeScheduledFixtureDevs(t, r)
	origin, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Source", Preset: "system"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Developer", Preset: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &scheduledManagerAdapter{r: r}
	prepareScheduledManagerRuntime(t, r, adapter)
	view := newConversationView(r, origin.ID, "main:"+origin.ID)
	view.delegation = d
	starts := 0
	d.reactor = reactor.NewScheduler(r.work, reactor.Hooks{
		Availability: func(context.Context) (work.Availability, error) {
			return work.Availability{Hosts: map[string]int{"local": 1}}, nil
		},
		Start: func(ctx context.Context, c work.Card) (work.Run, error) {
			starts++
			var assignment codingAssignment
			err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; assignment, err = readCoding(ctx, tx, c.Task.ID); return err })
			if err != nil || !assignment.Delegated || assignment.Owner != dev.ID || assignment.Origin.Agent != origin.ID || assignment.Origin.Conversation != view.conversation || assignment.Origin.Turn != "turn:managed-input" || c.Task.AgentID != dev.ID || c.Task.ConversationID != assignment.Conversation {
				t.Errorf("dispatch bypassed durable manager admission: task=%+v assignment=%+v error=%v", c.Task, assignment, err)
			}
			return work.Run{ID: c.Task.ID + ":1", TaskID: c.Task.ID, Generation: 1, Worker: "codex", ThreadID: "thread", TurnID: "turn", NodeID: "local"}, nil
		},
	})
	bindings := map[string]loop.Tool{}
	view.prepareDelegationTools("turn:managed-input", nil, bindings)
	call := provider.ToolCall{ID: "graph", Name: "schedule_work", Arguments: policyGraph}
	for range 2 {
		if result := bindings[call.Name].Execute(t.Context(), call); result.Code != loop.OK {
			t.Fatalf("graph admission failed: %+v; manager calls=%d error=%v", result, adapter.calls, adapter.err)
		}
	}
	if starts != 1 || adapter.calls != 1 {
		t.Fatalf("admission/start replayed or missing: starts=%d judgments=%d", starts, adapter.calls)
	}
}

func TestDelegationWithoutDevHasNoWorkerEffect(t *testing.T) {
	r, d, starts := delegationPolicyRuntime(t)
	removeScheduledFixtureDevs(t, r)
	origin, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Source", Preset: "creative"})
	if err != nil {
		t.Fatal(err)
	}
	view := newConversationView(r, origin.ID, "main:"+origin.ID)
	view.delegation = d
	bindings := map[string]loop.Tool{}
	view.prepareDelegationTools("turn:no-dev-input", nil, bindings)
	result := bindings["schedule_work"].Execute(t.Context(), provider.ToolCall{ID: "graph", Name: "schedule_work", Arguments: policyGraph})
	var body struct{ Code string }
	_ = json.Unmarshal(result.Output, &body)
	cards, err := r.work.Cards(t.Context())
	var effects int
	if e := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM authority_effects").Scan(&effects)
	}); e != nil {
		t.Fatal(e)
	}
	if result.Code != loop.Failed || body.Code != "dev_agent_unavailable" || err != nil || len(cards) != 0 || *starts != 0 || effects != 0 {
		t.Fatalf("missing dev reached worker effect: result=%+v body=%+v cards=%d starts=%d effects=%d err=%v", result, body, len(cards), *starts, effects, err)
	}
}

func TestDelegationManagerRefusalDoesNotAdmitGraph(t *testing.T) {
	r, d, starts := delegationPolicyRuntime(t)
	adapter := &scheduledManagerAdapter{r: r, decision: "unverified"}
	prepareScheduledManagerRuntime(t, r, adapter)
	view := newConversationView(r, r.agent, r.conversation)
	view.delegation = d
	bindings := map[string]loop.Tool{}
	view.prepareDelegationTools("turn:refused-input", nil, bindings)
	result := bindings["schedule_work"].Execute(t.Context(), provider.ToolCall{ID: "graph", Name: "schedule_work", Arguments: policyGraph})
	cards, err := r.work.Cards(t.Context())
	if result.Code != loop.Failed || adapter.calls != 1 || len(cards) != 0 || *starts != 0 || err != nil {
		t.Fatalf("manager refusal admitted work: result=%+v calls=%d cards=%d starts=%d err=%v", result, adapter.calls, len(cards), *starts, err)
	}
}

func removeScheduledFixtureDevs(t *testing.T, r *conversationRuntime) {
	t.Helper()
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE agents SET trashed_at=1 WHERE preset='dev'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func prepareScheduledManagerRuntime(t *testing.T, r *conversationRuntime, adapter *scheduledManagerAdapter) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	config, err := responses.BuildConfig()
	if err != nil {
		t.Fatal(err)
	}
	sqlite, err := manifest.SQLiteBuild()
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{Version: "0.0.1-alpha.dev", Commit: strings.Repeat("a", 40), Date: "2026-10-23", Target: runtime.GOOS + "/" + runtime.GOARCH, GoToolchain: runtime.Version(), KitVersion: 1, SupportedCombinations: []manifest.Combination{}, SQLite: sqlite}
	raw, err := json.Marshal(map[string]any{"chatgpt": config})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &m.Providers); err != nil {
		t.Fatal(err)
	}
	m.Binaries = []manifest.Binary{{Name: "lina", Size: int64(len(data)), SHA256: hash}, {Name: "lina-node", Size: int64(len(data)), SHA256: hash}}
	raw, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.root, "system", "bin")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	r.ownerViews.stop()
	<-r.ownerViews.done
	r.route, err = modelpath.ResponsesFromEnv(func(key string) string {
		return map[string]string{"LINA_RESPONSES_BASE_URL": "http://127.0.0.1:1/v1", "LINA_RESPONSES_MODEL": "fixture", "LINA_RESPONSES_API_KEY": "synthetic-key"}[key]
	})
	if err != nil {
		t.Fatal(err)
	}
	r.route.Adapter = adapter
	if err = r.startRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
}
