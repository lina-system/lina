// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestLiveExpandRefusesCopiesOfProtectedEvents(t *testing.T) {
	r, _ := turnRuntime(t)
	public := queuedContextEvent(t, r, "public-source", "The aurora plan is ready.", guard.EntryOwner, "")
	protected := queuedContextEvent(t, r, "protected-source", "The aurora private fact.", guard.EntryOwner, "")
	view := queuedContextEvent(t, r, "protected-view", "The aurora private fact, shown again.", guard.EntryOwner, "")
	copied, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "protected-copy", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, ReplayOf: protected.ID, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(2, 0), RecordedAt: time.Unix(2, 0), TimeQuality: "observed", Body: protected.Body.Text()})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO ev_rel(event_id,target_id,kind) VALUES(?,?,'view_of')`, view.ID, protected.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = coreexec.NewMemory(r.db, r.materials, nil).Remember(t.Context(), memory.Write{Person: r.person, Agent: r.agent, Item: memory.Item{Kind: memory.Fact, Content: protected.Body.Text(), Scope: memory.NeverLeaves, Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: protected.ID, Precision: memory.Exact}}}})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	if _, err := r.prepareRecallTools("expand-turn", nil, bindings); err != nil {
		t.Fatal(err)
	}
	// Ids come from conversation history: no search runs before expand.
	for _, e := range []struct {
		id      string
		allowed bool
	}{{public.ID, true}, {protected.ID, false}, {view.ID, false}, {copied.ID, false}} {
		args, _ := json.Marshal(map[string]any{"id": "event:" + e.id})
		out := bindings["expand"].Execute(t.Context(), provider.ToolCall{ID: e.id, Name: "expand", Arguments: string(args)})
		if (out.Code == loop.OK) != e.allowed {
			t.Errorf("expand %s: allowed=%v, got %+v", e.id, e.allowed, out)
		}
	}
}
