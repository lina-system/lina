// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestOwnerDigestAndControlsRegistered(t *testing.T) {
	r := controlRuntime(t)
	for _, kind := range []protocol.Kind{protocol.KindProgressDigestGet, protocol.KindFollowUp, protocol.KindMultiTaskControl} {
		if _, ok := r.identity.Operations[kind]; !ok {
			t.Errorf("Core operation %s missing", kind)
		}
	}
	if op, ok := r.identity.Operations[protocol.KindProgressDigestGet]; ok {
		if _, err := op.Handle(t.Context(), clientsrv.Session{PersonID: "foreign"}, protocol.Envelope{Payload: protocol.ProgressDigestGet{}}); err == nil {
			t.Fatal("foreign digest allowed")
		}
	}
}

func TestOwnerRunControlsScopeAndRepeatedNote(t *testing.T) {
	r := runRuntime(t)
	brief := runs.Brief{Goal: "inspect", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	start := func(conversation, person string) string {
		t.Helper()
		o := r.runOrigin("control")
		o.Conversation = conversation
		o.Person = person
		id, _, err := r.background.service.Start(t.Context(), o, brief)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	own := start(r.conversation, r.person)
	other := start("another-conversation", r.person)
	foreign := start(r.conversation, "foreign")
	s := clientsrv.Session{PersonID: r.person}
	note := protocol.Envelope{MessageID: "same-note", Payload: protocol.FollowUp{RunID: own, Text: "Read appendix", ConversationID: &r.conversation}}
	for range 2 {
		if _, err := r.followRun(t.Context(), s, note); err != nil {
			t.Fatal(err)
		}
	}
	record, _ := r.background.service.Get(t.Context(), own)
	if len(record.FollowUps) != 1 {
		t.Fatalf("note replayed: %+v", record.FollowUps)
	}
	for _, id := range []string{other, foreign} {
		if _, err := r.followRun(t.Context(), s, protocol.Envelope{Payload: protocol.FollowUp{RunID: id, Text: "No", ConversationID: &r.conversation}}); err == nil {
			t.Fatal("scope escaped")
		}
	}
	result, err := r.stopAllRuns(t.Context(), s, protocol.Envelope{Payload: protocol.MultiTaskControl{Action: "stop_all", ConversationID: &r.conversation}})
	if err != nil {
		t.Fatal(err)
	}
	if p := result.Payload.(protocol.MultiTaskControl); p.Count == nil || *p.Count != "1" {
		t.Fatalf("wrong stop count: %+v", p)
	}
	for _, id := range []string{own, other, foreign} {
		v, _ := r.background.service.Get(t.Context(), id)
		if (v.Outcome == runs.Cancelled) != (id == own) {
			t.Fatalf("wrong target stopped: %s %+v", id, v)
		}
	}
}

func TestCoreDigestCountsFullWaitingAndRereadsSources(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		for i := range 7 {
			id := fmt.Sprint(i)
			raw, _ := json.Marshal(authority.Approval{ID: id, Person: r.person, Agent: r.agent, Epoch: r.epoch, State: authority.Pending, What: "Choose\noption", Choices: []authority.Choice{authority.Once}})
			if _, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", id, id, "fixture", raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	get := func() protocol.ProgressDigest {
		t.Helper()
		v, err := r.progressDigest(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.ProgressDigestGet{}})
		if err != nil {
			t.Fatal(err)
		}
		return v.Payload.(protocol.ProgressDigest)
	}
	d := get()
	if len(d.Lines) != 5 || d.Total != "7" || d.More != "2" {
		t.Fatalf("bad digest: %+v", d)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE authority_approvals SET record=json_set(record,'$.State','approved')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d = get()
	if len(d.Lines) != 0 || d.Total != "0" || d.More != "0" {
		t.Fatalf("stale digest: %+v", d)
	}
}

func TestOwnerControlsReachLiveWorker(t *testing.T) {
	r := runRuntime(t)
	probe := &workerProbe{snapshot: worker.ThreadSnapshot{Thread: worker.Thread{ID: "live", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}, State: worker.Active}}
	entry := &workerSession{origin: r, adapter: probe, thread: probe.snapshot.Thread, active: map[string]lease.Lease{}}
	r.workers = &workerRuntime{sessions: map[string]*workerSession{"live": entry}}
	t.Cleanup(func() { r.workers = nil })
	s := clientsrv.Session{PersonID: r.person}
	id := "session:live"
	if _, err := r.followRun(t.Context(), s, protocol.Envelope{Payload: protocol.FollowUp{RunID: id, Text: "Read appendix", ConversationID: &r.conversation}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.stopRun(t.Context(), s, protocol.Envelope{Payload: protocol.Stop{RunID: &id, ConversationID: &r.conversation}}); err != nil {
		t.Fatal(err)
	}
	if probe.inputs != 1 || probe.interrupts != 1 {
		t.Fatalf("worker did not receive controls: %+v", probe)
	}
}

func TestForwardedOwnerCommandRemainsEvidence(t *testing.T) {
	c := &channelRuntime{}
	handled, err := c.channelControl(t.Context(), nil, gateway.Inbound{Body: []byte(`{"text":"/stop all","forwarded":true}`)}, channels.Binding{}, channelInput{})
	if err != nil || handled {
		t.Fatalf("forwarded text became control: %t %v", handled, err)
	}
}
