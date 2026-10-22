// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestChannelNoteRetainsRevocableBinding(t *testing.T) {
	for _, change := range []string{"unbind", "disable-tools"} {
		t.Run(change, func(t *testing.T) { testChannelNoteScope(t, change) })
	}
}
func testChannelNoteScope(t *testing.T, change string) {
	r := runRuntime(t)
	store := channels.New(r.db)
	bot := channels.Bot{Platform: "telegram", Account: "scope-fixture"}
	now := time.Now()
	if err := store.Connect(t.Context(), bot, r.person); err != nil {
		t.Fatal(err)
	}
	code, err := store.PairingCode(t.Context(), bot, r.person, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Pair(t.Context(), bot, code, "42", "42", now); err != nil {
		t.Fatal(err)
	}
	b := channels.Binding{Address: channels.Address{Bot: bot, Chat: "42"}, Person: r.person, Agent: r.agent, Conversation: r.conversation, Scope: channels.PrivateScope()}
	if err = store.Bind(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	id, _, err := r.background.service.Start(t.Context(), r.runOrigin("app-start"), runs.Brief{Goal: "inspect", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"text": "/note " + id + " Read appendix"})
	receipt, err := inbox.New(r.db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: bot.Key(), NativeID: "note"}, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	event := gateway.Inbound{Address: b.Address, Direct: true, Sender: "42", Body: body}
	if handled, err := r.channels.channelControl(t.Context(), r, event, b, channelInput{id: receipt.ID, native: "note"}); err != nil || !handled {
		t.Fatalf("note: %t %v", handled, err)
	}
	record, err := r.background.service.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(record)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var sources []json.RawMessage
	if json.Unmarshal(fields["FollowSources"], &sources) != nil || len(sources) != 1 {
		t.Fatal("queued channel note lost its source binding")
	}
	if change == "unbind" {
		err = store.Unbind(t.Context(), b.Address, r.person)
	} else {
		b.Scope.Tools = false
		err = store.Bind(t.Context(), b)
	}
	if err != nil {
		t.Fatal(err)
	}
	// runRuntime's provider fails the test if any model request reaches it.
	if err = r.background.service.Release(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err = r.background.service.Wait(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	record, err = r.background.service.Get(t.Context(), id)
	if err != nil || record.Outcome != runs.Failed {
		t.Fatalf("revoked instruction dispatched: %+v %v", record, err)
	}
}

func TestOwnerControlsReachManagedChannelWorker(t *testing.T) {
	r := runRuntime(t)
	delivery := newConversationView(r, r.agent, "channel:shared")
	manager := newConversationView(r, r.agent, "manager:work")
	manager.codingDelivery = delivery
	probe := &workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "managed", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}, State: worker.Active}}
	r.workers.sessions["managed"] = &workerSession{origin: manager, adapter: probe, thread: probe.snapshot.Thread, active: map[string]lease.Lease{}}
	t.Cleanup(func() { r.workers = nil })
	s := clientsrv.Session{PersonID: r.person}
	id := "session:managed"
	for _, scope := range []string{"channel:other", delivery.conversation} {
		_, err := r.followRun(t.Context(), s, protocol.Envelope{MessageID: fmt.Sprint("note-", scope), Payload: protocol.FollowUp{RunID: id, Text: "Read appendix", ConversationID: &scope}})
		if (err == nil) != (scope == delivery.conversation) {
			t.Fatalf("delivery scope %s: %v", scope, err)
		}
	}
	if _, err := r.stopRun(t.Context(), s, protocol.Envelope{Payload: protocol.Stop{RunID: &id, ConversationID: &delivery.conversation}}); err != nil {
		t.Fatal(err)
	}
	result, err := r.stopAllRuns(t.Context(), s, protocol.Envelope{Payload: protocol.MultiTaskControl{Action: "stop_all", ConversationID: &delivery.conversation}})
	if err != nil {
		t.Fatal(err)
	}
	if n := result.Payload.(protocol.MultiTaskControl).Count; n == nil || *n != "1" {
		t.Fatalf("managed worker omitted: %+v", result)
	}
	if probe.inputs != 1 || probe.interrupts != 2 {
		t.Fatalf("controls lost: input=%d interrupt=%d", probe.inputs, probe.interrupts)
	}
}
