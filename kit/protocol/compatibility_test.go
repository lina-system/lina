// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestOlderSchemaClientReceivesPresentationAdditions(t *testing.T) {
	// Regenerate a client without the activity and running-presentation
	// additions. It gets today's receive boundary, not today's payload types.
	// This cannot repair an already installed binary with a strict receiver.
	data, err := os.ReadFile("../../protocol/schema/client/1.0/envelope.json")
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]json.RawMessage
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	var defs map[string]json.RawMessage
	if err := json.Unmarshal(old["$defs"], &defs); err != nil {
		t.Fatal(err)
	}
	removeKind := func(data []byte) []byte {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(data, &object)
		var properties map[string]json.RawMessage
		_ = json.Unmarshal(object["properties"], &properties)
		var kind map[string]json.RawMessage
		_ = json.Unmarshal(properties["kind"], &kind)
		var kinds []string
		_ = json.Unmarshal(kind["enum"], &kinds)
		kind["enum"], _ = json.Marshal(slices.DeleteFunc(kinds, func(k string) bool { return k == "tool_activity" }))
		properties["kind"], _ = json.Marshal(kind)
		object["properties"], _ = json.Marshal(properties)
		var rules []json.RawMessage
		_ = json.Unmarshal(object["allOf"], &rules)
		rules = slices.DeleteFunc(rules, func(data json.RawMessage) bool {
			var rule struct {
				If struct {
					Properties map[string]struct{ Const string }
				}
			}
			_ = json.Unmarshal(data, &rule)
			return rule.If.Properties["kind"].Const == "tool_activity"
		})
		object["allOf"], _ = json.Marshal(rules)
		result, _ := json.Marshal(object)
		return result
	}
	var turn map[string]json.RawMessage
	_ = json.Unmarshal(defs["turn_state"], &turn)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(turn["properties"], &fields)
	delete(fields, "phase")
	delete(fields, "tool_name")
	turn["properties"], _ = json.Marshal(fields)
	defs["turn_state"], _ = json.Marshal(turn)
	var running map[string]json.RawMessage
	_ = json.Unmarshal(defs["running_update"], &running)
	var runningFields map[string]json.RawMessage
	_ = json.Unmarshal(running["properties"], &runningFields)
	for _, name := range []string{"state", "summary", "reason"} {
		delete(runningFields, name)
	}
	running["properties"], _ = json.Marshal(runningFields)
	delete(running, "allOf")
	defs["running_update"], _ = json.Marshal(running)
	defs["stream_item"] = removeKind(defs["stream_item"])
	for _, name := range []string{"turn_phase", "tool_activity", "tool_activity_status", "tool_activity_outcome", "running_state", "running_reason"} {
		delete(defs, name)
	}
	old["$defs"], _ = json.Marshal(defs)
	data, _ = json.Marshal(old)
	data = removeKind(data)
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("old-schema.json", data)
	write("go.mod", []byte("module github.com/thisisjun786/lina/kit/protocol\n\ngo 1.27\n"))
	for _, name := range []string{"protocol.go", "receive.go", "internal/schema/schema.go"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(name, data)
	}
	cmd := exec.Command("go", "run", "./internal/generate", "-schema", filepath.Join(dir, "old-schema.json"), "-output", filepath.Join(dir, "client.gen.go"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate old schema: %v\n%s", err, output)
	}
	fixturePath, err := filepath.Abs(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	write("compatibility_test.go", []byte(oldClientTest))
	cmd = exec.Command("go", "test", "-count=1", "-v", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "PROTOCOL_FIXTURES="+fixturePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("old-schema client: %v\n%s", err, output)
	}
}

const oldClientTest = `// SPDX-License-Identifier: Apache-2.0
package protocol
import ("encoding/json"; "os"; "path/filepath"; "reflect"; "slices"; "testing")
func TestOldClient(t *testing.T) {
 if _, present := reflect.TypeFor[TurnState]().FieldByName("Phase"); present { t.Fatal("test client knows phase") }
 if _, present := reflect.TypeFor[TurnState]().FieldByName("ToolName"); present { t.Fatal("test client knows tool_name") }
 for _, name := range []string{"State", "Summary", "Reason"} {
  if _, present := reflect.TypeFor[RunningUpdate]().FieldByName(name); present { t.Fatalf("test client knows running %s", name) }
 }
 if slices.Contains(clientSchema.Properties["kind"].Enum, "tool_activity") { t.Fatal("test client knows tool_activity") }
 var client ClientDecoder
 for _, name := range []string{"phase_tool", "tool_started", "tool_finished_ok", "activity_snapshot", "phase_snapshot", "phase_streaming", "item_done", "running_localized", "running_snapshot"} {
  data, err := os.ReadFile(filepath.Join(os.Getenv("PROTOCOL_FIXTURES"), "conversation", name+".json")); if err != nil { t.Fatal(err) }
  got, err := client.Decode(data); if err != nil { t.Fatalf("%s: %v", name, err) }
  if name == "tool_started" || name == "tool_finished_ok" { if got != nil { t.Fatal("unknown tool activity was dispatched") }; continue }
  if got == nil { t.Fatalf("%s: known event ignored", name) }
  if name == "phase_tool" || name == "phase_streaming" {
   turn, ok := got.Payload.(TurnState); if !ok || turn.Active == nil || !*turn.Active || turn.AgentID == nil || *turn.AgentID != "agent-example" { t.Fatalf("known turn state lost: %#v", got) }
   var strict Envelope; if json.Unmarshal(data, &strict) == nil { t.Fatal("old strict codec accepted new fields") }
  }
  if name == "activity_snapshot" {
   snapshot := got.Payload.(Snapshot)
   if len(snapshot.Items) != 1 || snapshot.Items[0].Kind != KindItemDone || string(snapshot.Cursor.Seq) != "2" { t.Fatalf("unknown snapshot item not skipped: %#v", snapshot) }
  }
  if name == "phase_snapshot" {
   snapshot := got.Payload.(Snapshot)
   if len(snapshot.Items) != 2 || snapshot.Items[1].Kind != KindTurnState { t.Fatalf("turn snapshot lost: %#v", snapshot) }
   var turn TurnState
   if err := json.Unmarshal(snapshot.Items[1].Payload, &turn); err != nil || turn.Active == nil || !*turn.Active { t.Fatalf("known snapshot state lost: %#v, %v", turn, err) }
  }
  if name == "running_localized" || name == "running_snapshot" {
   var row RunningUpdate
   if name == "running_localized" { row = got.Payload.(RunningUpdate) } else {
    snapshot := got.Payload.(Snapshot)
    if len(snapshot.Items) != 1 { t.Fatal("running row lost") }
    if err := json.Unmarshal(snapshot.Items[0].Payload, &row); err != nil { t.Fatal(err) }
   }
   if row.Text == nil || *row.Text != "waiting: Synthetic task (quota)" || row.RunID == nil || *row.RunID != "run-example" {
    t.Fatalf("older client lost legacy running text or identity: %+v", row)
   }
  }
  if _, err := json.Marshal(got); err != nil { t.Fatalf("old decoded envelope cannot be encoded: %v", err) }
 }
 if client.Stats() != (DecodeStats{Accepted:7, UnknownKinds:2}) { t.Fatalf("stats: %+v", client.Stats()) }
}
`
