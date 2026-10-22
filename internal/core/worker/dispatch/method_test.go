// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	builtin "github.com/thisisjun786/lina/skills"
)

func TestDispatchMethodPacketAndUnknownReference(t *testing.T) {
	db := store(t)
	catalog, err := skills.Open(t.TempDir(), db, builtin.Builtins())
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	s, err := Open(t.Context(), Config{DB: db, Skills: catalog})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	tool := s.Tool(Origin{Person: "person", Agent: "agent", Conversation: "conversation", Turn: "turn:input"})
	d := definition(t)
	raw, _ := json.Marshal(d)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["kind"] = "bug_unknown_cause"
	call := func(id string) provider.ToolCall {
		b, _ := json.Marshal(fields)
		return provider.ToolCall{ID: id, Name: "dispatch_work", Arguments: string(b)}
	}
	for _, bad := range []any{"missing-skill", []string{"qa", "perf"}, "../qa"} {
		fields["skill"] = bad
		if _, err := tool.Validate(call("bad")); err == nil {
			t.Fatalf("validated bad reference %v", bad)
		}
		if result := tool.Execute(t.Context(), call("bad")); result.Code != loop.Invalid {
			t.Fatalf("accepted bad reference: %+v", result)
		}
	}
	delete(fields, "skill")
	accepted := tool.Execute(t.Context(), call("good"))
	if accepted.Code != loop.OK {
		t.Fatal(accepted)
	}
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("records %+v %v", records, err)
	}
	r := records[0]
	if r.Definition.Skill != "app-debugging" || r.Method == nil {
		t.Fatalf("unresolved method %+v", r)
	}
	expected, err := catalog.Read(t.Context(), r.Method.Version)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := taskPacket(r)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Task   string      `json:"task_packet"`
		Method skills.Body `json:"method"`
	}
	if err = json.Unmarshal([]byte(packet), &wire); err != nil || wire.Method.Files["SKILL.md"].Text != expected.Files["SKILL.md"].Text || !strings.Contains(wire.Task, d.What) {
		t.Fatalf("packet did not carry body and definition: %s %v", packet, err)
	}
	replacement := "---\nname: app-debugging\ndescription: Synthetic change.\n---\nNew procedure.\n"
	if _, err = catalog.Patch(t.Context(), skills.Patch{ID: "app-debugging", Files: skills.Bundle{"SKILL.md": {Text: replacement}}}); err != nil {
		t.Fatal(err)
	}
	if replay := tool.Execute(t.Context(), call("good")); string(replay.Output) != string(accepted.Output) || replay.Code != loop.OK {
		t.Fatal("retry changed admission", replay)
	}
	records, err = s.records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	same, err := taskPacket(records[0])
	if err != nil || same != packet {
		t.Fatal("queued packet drifted", err)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A restarted dispatcher can use the persisted body without rereading an
	// updated catalog or installing anything in the worker's filesystem.
	restarted, err := Open(t.Context(), Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	records, err = restarted.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("restart: %+v %v", records, err)
	}
	same, err = taskPacket(records[0])
	if err != nil || same != packet || records[0].State != "queued" {
		t.Fatal("restart lost the queued snapshot", err)
	}
}
