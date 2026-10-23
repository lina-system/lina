// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestHandoffToolsTrackReceiptAndCheckback(t *testing.T) {
	r := controlRuntime(t)
	peer, err := r.agents.Create(t.Context(), agents.Create{Name: "Peer", PersonID: r.person})
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]loop.Tool{}
	if len(r.prepareExchangeTools("turn", bindings)) == 0 {
		t.Fatal("missing handoff tools")
	}
	now := time.Now()
	args, _ := json.Marshal(map[string]any{"to": peer.ID, "handoff": exchange.HandOff{Subject: "Check draft", Wanted: "Read draft", OutOfScope: "No publishing", Done: "Draft checked", Check: "source", Deadline: now.Add(time.Hour)}, "next_check": now.Add(time.Minute)})
	call := provider.ToolCall{ID: "send", Name: "send_handoff", Arguments: string(args)}
	result := bindings[call.Name].Execute(t.Context(), call)
	if result.Code != loop.OK {
		t.Fatalf("send: %+v", result)
	}
	var status exchange.Status
	if err := json.Unmarshal(result.Output, &status); err != nil {
		t.Fatal(err)
	}
	if status.Delivery != "pending" || status.Waiting != "waiting" {
		t.Fatalf("premature sent: %+v", status)
	}
	store := exchange.NewWithMaterials(r.db, r.materials)
	if _, err := store.Deliver(t.Context(), status.DeliveryID, now); err != nil {
		t.Fatal(err)
	}
	status, err = store.Status(t.Context(), status.ID, r.person, r.agent)
	if err != nil || status.Delivery != "sent" {
		t.Fatalf("receipt: %+v %v", status, err)
	}
	if err := store.Raise(t.Context(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	notices, err := store.Notices(t.Context(), r.person)
	if err != nil || len(notices) != 1 || notices[0].Reason != "next_check" {
		t.Fatalf("checkback: %+v %v", notices, err)
	}
	if err := store.Report(t.Context(), status.DeliveryID, peer.ID, exchange.Answer{Text: "Checked draft", Evidence: []exchange.Evidence{{EventID: "draft-event", At: now}}}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Raise(t.Context(), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	notices, err = store.Notices(t.Context(), r.person)
	if err != nil || len(notices) != 2 {
		t.Fatalf("report after due: %+v %v", notices, err)
	}
	foreign := newConversationView(r, peer.ID, "main:"+peer.ID)
	other := map[string]loop.Tool{}
	foreign.prepareExchangeTools("other", other)
	raw, _ := json.Marshal(map[string]string{"id": status.ID})
	if got := other["handoff_status"].Execute(t.Context(), provider.ToolCall{ID: "read", Arguments: string(raw)}); got.Code == loop.OK {
		t.Fatal("foreign sender status exposed")
	}
}

func TestArtifactOwnerRoutesSecondRecordWrite(t *testing.T) {
	r := controlRuntime(t)
	bindings := map[string]loop.Tool{}
	if err := r.prepareRecordTools("create", bindings); err != nil {
		t.Fatal(err)
	}
	create := provider.ToolCall{ID: "create", Name: "write_record", Arguments: `{"id":"","revision":"","kind":"document","title":"Draft","note":"Original"}`}
	got := bindings[create.Name].Execute(t.Context(), create)
	if got.Code != loop.OK {
		t.Fatalf("create: %+v", got)
	}
	var doc struct{ ID, Revision string }
	if err := json.Unmarshal(got.Output, &doc); err != nil {
		t.Fatal(err)
	}
	r.prepareExchangeTools("lease", bindings)
	args, _ := json.Marshal(changeRequest{Artifact: exchange.Artifact{Kind: "business_document", ID: "record:" + doc.ID}, Done: "Draft revised", Wanted: "Revise", OutOfScope: "No sending", NextCheck: time.Now().Add(time.Hour), Deadline: time.Now().Add(2 * time.Hour)})
	got = bindings["begin_artifact_change"].Execute(t.Context(), provider.ToolCall{ID: "lease", Arguments: string(args)})
	var lease exchange.ChangeResult
	if got.Code != loop.OK || json.Unmarshal(got.Output, &lease) != nil || lease.Change == nil {
		t.Fatalf("lease: %+v", got)
	}
	// A later duplicate preset must not displace an already open owner.
	if _, err := r.agents.Create(t.Context(), agents.Create{Name: "Second coordinator", Preset: "lina", PersonID: r.person}); err != nil {
		t.Fatal(err)
	}
	same := bindings["begin_artifact_change"].Execute(t.Context(), provider.ToolCall{ID: "same-artifact", Arguments: string(args)})
	if same.Code != loop.OK {
		t.Fatalf("existing owner lost to preset ambiguity: %+v", same)
	}
	owner, err := r.artifactOwner(t.Context(), "wiki")
	if err != nil {
		t.Fatal(err)
	}
	other := newConversationView(r, owner, "main:"+owner)
	otherBindings := map[string]loop.Tool{}
	if err := other.prepareRecordTools("edit", otherBindings); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"id": doc.ID, "revision": doc.Revision, "kind": "document", "title": "Draft", "note": "Other edit"})
	call := provider.ToolCall{ID: "edit", Name: "write_record", Arguments: string(raw)}
	for range 2 {
		result := otherBindings[call.Name].Execute(t.Context(), call)
		if result.Code != loop.OK {
			t.Fatalf("route: %+v", result)
		}
		var routed struct {
			Handoff exchange.Status
			Changed bool
		}
		if json.Unmarshal(result.Output, &routed) != nil || routed.Handoff.ID == "" || routed.Changed {
			t.Fatalf("parallel change: %+v", result)
		}
	}
	_, revision, err := r.records.Read(t.Context(), doc.ID)
	if err != nil || revision != doc.Revision {
		t.Fatalf("record changed: %s %v", revision, err)
	}
	rows, err := r.exchangeStore().Log(t.Context())
	if err != nil || len(rows) != 1 || rows[0].To != r.agent {
		t.Fatalf("handoffs: %+v %v", rows, err)
	}
	if err := r.exchangeStore().EndChange(t.Context(), r.person, owner, lease.Change.WorkID); err == nil {
		t.Fatal("foreign close")
	}
	if err := r.exchangeStore().EndChange(t.Context(), r.person, r.agent, lease.Change.WorkID); err != nil {
		t.Fatal(err)
	}
	result := otherBindings[call.Name].Execute(t.Context(), provider.ToolCall{ID: "after-release", Name: call.Name, Arguments: call.Arguments})
	if result.Code != loop.OK {
		t.Fatalf("release did not unblock write: %+v", result)
	}
}
