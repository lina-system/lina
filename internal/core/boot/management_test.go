// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/kit/protocol"
)

func managementFixture(t *testing.T) (*conversationRuntime, clientsrv.Session, string, string) {
	t.Helper()
	// Growth starts jobs.sock here; keep test names out of its bounded Unix path.
	root, err := os.MkdirTemp("", "lina-management-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	store, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	first, err := store.FirstRun(t.Context(), "person", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(t.Context(), agents.Create{Name: "second", PersonID: "person", Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removals.Close() })
	return &conversationRuntime{root: root, controls: controls, main: "fixture", epoch: 1, db: db, agents: store, removals: removals, person: "person", lanes: lane.New()}, clientsrv.Session{PersonID: "person"}, first.Agent.ID, second.ID
}
func textptr(s string) *string { return &s }

func TestChosenAgentResumesAfterStoreReopen(t *testing.T) {
	r, session, first, chosen := managementFixture(t)
	r.agent = first
	router := &conversationRouter{root: r, views: map[string]*conversationRuntime{}}
	for _, id := range []string{first, chosen} {
		router.views[id] = &conversationRuntime{person: r.person, agent: id, conversation: "main:" + id}
	}
	if _, err := r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("choose"), AgentID: &chosen}); err != nil {
		t.Fatal(err)
	}
	store, err := agents.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r.agents = store
	history, err := router.History(t.Context(), session, protocol.HistoryGet{})
	if err != nil || history.AgentID == nil || *history.AgentID != chosen {
		t.Fatalf("restart lost the last conversation: %+v %v", history, err)
	}
	// A deleted selection cannot redirect a newly opened surface to an older
	// runtime view. Use the original default, independently of boot's focus.
	r.agent = chosen
	if err := r.agents.Delete(t.Context(), chosen, session.PersonID, time.Now()); err != nil {
		t.Fatal(err)
	}
	history, err = router.History(t.Context(), session, protocol.HistoryGet{})
	if err != nil || history.AgentID == nil || *history.AgentID != first {
		t.Fatalf("deleted selection did not fall back to the default: %+v %v", history, err)
	}
}
func TestManagementBindsPersonAndScopesTargets(t *testing.T) {
	r, session, a, b := managementFixture(t)
	saved, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("remember"), AgentID: &a, Holder: textptr("agent"), Text: textptr("fixture private statement")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		session clientsrv.Session
		p       protocol.MemoryRequest
		code    protocol.ErrorCode
	}{
		{"person", clientsrv.Session{PersonID: "another"}, protocol.MemoryRequest{Operation: textptr("list"), AgentID: &a}, protocol.ErrorNotAuthorized},
		{"other holder", session, protocol.MemoryRequest{Operation: textptr("forget"), AgentID: &b, ItemID: saved.ItemID}, protocol.ErrorOutOfScope},
		{"missing id", session, protocol.MemoryRequest{Operation: textptr("correct"), AgentID: &a, Text: textptr("replacement")}, protocol.ErrorMalformed},
		{"foreign holder override", session, protocol.MemoryRequest{Operation: textptr("remember"), AgentID: &a, Holder: &b, Text: textptr("replacement")}, protocol.ErrorMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Memory(t.Context(), tc.session, tc.p)
			var refusal *clientsrv.OperationError
			if !errors.As(err, &refusal) || refusal.Code != tc.code {
				t.Fatalf("refusal=%v", err)
			}
		})
	}
	listed, err := r.Memory(t.Context(), session, protocol.MemoryRequest{Operation: textptr("list"), AgentID: &a})
	if err != nil || listed.Items == nil || len(*listed.Items) != 1 {
		t.Fatal("refused mutation changed canon", err)
	}
}
func TestDeletingDefaultLeavesExistingStoreUsable(t *testing.T) {
	r, session, _, b := managementFixture(t)
	renamed, err := r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("rename"), AgentID: &b, Name: textptr("renamed")})
	if err != nil || renamed.AgentID == nil || *renamed.AgentID != b {
		t.Fatal(err)
	}
	all, err := r.agents.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range all {
		id := row.ID
		if _, err := r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("delete"), AgentID: &id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.agents.FirstRun(t.Context(), session.PersonID, true); !errors.Is(err, agents.ErrNotFound) {
		t.Fatal("deleted default was recreated", err)
	}
	list, err := r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("list")})
	if err != nil || list.Agents == nil || len(*list.Agents) != 0 {
		t.Fatal("trash remained listed", err)
	}
	created, err := r.Agent(t.Context(), session, protocol.AgentRequest{Operation: textptr("create"), Name: textptr("replacement")})
	if err != nil || created.AgentID == nil || len(*created.Agents) != 1 {
		t.Fatal("empty list cannot create", err)
	}
}
