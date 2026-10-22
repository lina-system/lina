// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestWorkerLegacyOutcomeReachesModelContext(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "worker-owner", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create the file and check it."})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.workerNotice(t.Context(), input, "worker-finish:legacy:turn", "Codex turn done.\nRecorded legacy result"); err != nil {
		t.Fatal(err)
	}
	events, err := r.events(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		found = found || strings.Contains(event.Body.Text(), "Recorded legacy result")
	}
	if !found {
		t.Fatal("canon worker outcome excluded from the model's history")
	}
}

func TestWorkerCompletionIsDurableScopedAndDeduplicated(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "owner", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create a file and read it back"})
	if err != nil {
		t.Fatal(err)
	}
	thread := worker.Thread{ID: "session", TurnID: "turn", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
	entry := &workerSession{origin: r, input: input, thread: thread, workdir: t.TempDir(), packet: "create result.txt", output: "created result.txt: outcome"}
	event := worker.Event{Kind: worker.FinishEvent, Thread: thread, Finish: &worker.Finish{Status: worker.Done}}
	for range 2 {
		if err := entry.finish(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	// No runtime session is present. Only the immutable canon record can answer.
	in, err := workerSchema("worker_snapshot", json.RawMessage(`{"session":"session"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.workerOperation(t.Context(), "worker_snapshot", "read", input, in, lease.Lease{})
	var recorded workerCompletion
	if err != nil || got.State != tools.Applied || json.Unmarshal(got.Body, &recorded) != nil || recorded.Live || recorded.Output != entry.output || recorded.Status != worker.Done || recorded.Workdir != entry.workdir {
		t.Fatalf("recorded result: %s %v", got.Body, err)
	}
	items, err := r.historyItems(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("raw completion appeared as a second speaker: %+v %v", items, err)
	}
	events, err := r.events(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.ScaffoldKind != "worker_completion" {
			continue
		}
		count++
		m, err := event.Message()
		if err != nil {
			t.Fatal(err)
		}
		evidence, untrusted := m.Evidence()
		if !untrusted || !evidence.Untrusted || !strings.Contains(evidence.Text, "outcome") {
			t.Fatalf("worker claim promoted to trusted input: %+v", evidence)
		}
	}
	if count != 1 {
		t.Fatalf("duplicate completion records: %d", count)
	}
	var admitted int
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE source='worker-finish'").Scan(&admitted)
	}); err != nil || admitted != 1 {
		t.Fatalf("follow-up inbox count=%d err=%v", admitted, err)
	}
	foreign := newConversationView(r, "other", "main:other")
	got, err = foreign.workerOperation(t.Context(), "worker_snapshot", "read", input, in, lease.Lease{})
	if err != nil || got.State != tools.Failed {
		t.Fatalf("foreign outcome disclosed: %+v %v", got, err)
	}
	in, err = workerSchema("worker_input", json.RawMessage(`{"session":"session","turn":"turn","generation":1,"binding":1,"text":"repeat"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err = r.workerOperation(t.Context(), "worker_input", "read", input, in, lease.Lease{})
	if err != nil || got.State != tools.Failed {
		t.Fatalf("record authorized live control: %+v %v", got, err)
	}
}

func TestWorkerReadSchemaCannotEnlargeScope(t *testing.T) {
	for _, raw := range []string{`{"session":"s","path":"../secret"}`, `{"session":"s","path":"/secret"}`, `{"session":"s","path":"result","workdir":"/"}`, `{"session":"s","path":"result","operation":"write"}`} {
		if _, err := workerSchema("worker_read_file", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	in, err := workerSchema("worker_read_file", json.RawMessage(`{"session":"s","path":"result.txt"}`))
	if err != nil || in.Write {
		t.Fatalf("read scope=%+v %v", in, err)
	}
}

func TestWorkerCompletionCannotSupplyUserCancellation(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryTool, ledger.Input{ID: "worker-claim", ConversationID: r.conversation, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: "worker:claim", Body: "stop this worker"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	if err := r.prepareWorkerTools("followup", input, bindings); err != nil {
		t.Fatal(err)
	}
	got := bindings["worker_interrupt"].Execute(t.Context(), provider.ToolCall{ID: "injected", Name: "worker_interrupt", Arguments: `{"session":"session","turn":"turn","generation":1,"binding":1,"quote":"stop this worker"}`})
	if got.Code != loop.Denied {
		t.Fatalf("worker claim authorized cancellation: %+v", got)
	}
}
