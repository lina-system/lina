// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func queuedContextEvent(t *testing.T, r *conversationRuntime, id, body string, entry guard.Entry, scaffold string) ledger.Event {
	t.Helper()
	person := ""
	if entry == guard.EntryOwner {
		person = r.person
	}
	event, err := r.journal().Admit(t.Context(), entry, ledger.Input{
		ID: id, ConversationID: r.conversation, AgentID: r.agent, PersonID: person,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0),
		TimeQuality: "observed", Body: body, ScaffoldKind: scaffold,
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func forgetQueuedSource(t *testing.T, r *conversationRuntime, id string) {
	t.Helper()
	receipt, err := memoryexec.NewMemory(r.db, nil, nil).Remember(t.Context(), memory.Write{
		Person: r.person, Agent: r.agent, Item: memory.Item{
			Kind: memory.Fact, Content: "Fixture fact from " + id,
			Subject: memory.Subject{Kind: memory.Person, ID: r.person}, Basis: memory.Stated,
			Evidence: []memory.Evidence{{Kind: memory.Event, ID: id, Precision: memory.Exact}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.removals.Forget(t.Context(), retract.Request{Person: r.person, Targets: []retract.Ref{{Kind: retract.Memory, ID: receipt.ID}}}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedContextKeepsHistoryAndCurrentIdentity(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	queuedContextEvent(t, r, "earlier", "Repeated input", guard.EntryOwner, "")
	queuedContextEvent(t, r, "forgotten", "Forgotten input", guard.EntryOwner, "")
	current := queuedContextEvent(t, r, "current", "Repeated input", guard.EntryOwner, "")
	queuedContextEvent(t, r, "future", "Future queued input", guard.EntryOwner, "")
	queuedContextEvent(t, r, "reply", "Prior turn reply", guard.EntryAgent, "")
	queuedContextEvent(t, r, "notice", "Prior turn notice", guard.EntrySystem, "run_handoff")
	forgetQueuedSource(t, r, "forgotten")
	items, _, _, events, err := r.modelContext(t.Context(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if !reflect.DeepEqual(ids, []string{"earlier", "reply", "notice", "current"}) {
		t.Fatalf("visible context lost history, identity or retraction: %v", ids)
	}
	want, err := assemble.History(events, r.conversation, r.agent)
	if err != nil || !reflect.DeepEqual(items, want) {
		t.Fatalf("context projection differs from its event evidence: %s, %v", items, err)
	}
	if len(items) != 4 || !bytes.Equal(items[0], items[3]) {
		t.Fatalf("distinct events with identical text were not both retained: %s", items)
	}
}

func TestQueuedContextRejectsMissingCurrentIdentity(t *testing.T) {
	for _, retractCurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "retracted"}[retractCurrent], func(t *testing.T) {
			r, _, agent, _ := managementFixture(t)
			r.agent, r.conversation = agent, "main:"+agent
			queuedContextEvent(t, r, "earlier", "Repeated input", guard.EntryOwner, "")
			if retractCurrent {
				queuedContextEvent(t, r, "current", "Repeated input", guard.EntryOwner, "")
				forgetQueuedSource(t, r, "current")
			}
			items, _, _, events, err := r.modelContext(t.Context(), "current")
			if !errors.Is(err, assemble.ErrHistory) {
				t.Fatalf("missing current identity accepted: err=%v events=%d items=%s", err, len(events), items)
			}
		})
	}
}
