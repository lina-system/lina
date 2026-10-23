// SPDX-License-Identifier: Apache-2.0
package acp_test

import (
	"bytes"
	"encoding/json"
	"github.com/thisisjun786/lina/kit/acp"
	"os/exec"
	"reflect"
	"testing"
)

func TestGeneratedTypesReproduce(t *testing.T) {
	out, err := exec.Command("node", "../../third_party/acp/generate.mjs", "--check").CombinedOutput()
	if err != nil {
		t.Fatalf("schema regeneration: %v\n%s", err, out)
	}
}

func TestWireTypesRetainUnionPayloads(t *testing.T) {
	cases := []struct {
		name, raw string
		value     any
	}{
		{"tool", `{"sessionUpdate":"tool_call","toolCallId":"t","title":"Write","status":"in_progress","kind":"edit","rawInput":{"n":9007199254740993},"_meta":{"vendor":true}}`, new(acp.SessionUpdate)},
		{"plan", `{"sessionUpdate":"plan","entries":[{"content":"Write result","priority":"high","status":"pending"}]}`, new(acp.SessionUpdate)},
		{"usage", `{"sessionUpdate":"usage_update","size":1000,"used":50,"cost":{"amount":0.1,"currency":"USD"}}`, new(acp.SessionUpdate)},
		{"config", `{"id":"model","name":"Model","type":"select","currentValue":"a","options":[{"value":"a","name":"A"}]}`, new(acp.SessionConfigOption)},
		{"elicitation", `{"action":"accept","content":{"answer":"yes"}}`, new(acp.CreateElicitationResponse)},
		{"unknown", `{"sessionUpdate":"future","n":9007199254740993}`, new(acp.SessionUpdate)},
		{"request-id", `9007199254740993`, new(acp.RequestID)},
		{"string-id", `"9007199254740993"`, new(acp.RequestID)},
		{"extension", `{"nested":{"number":9007199254740993}}`, new(acp.ExtRequest)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if e := json.Unmarshal([]byte(tc.raw), tc.value); e != nil {
				t.Fatal(e)
			}
			b, e := json.Marshal(tc.value)
			if e != nil {
				t.Fatal(e)
			}
			decode := func(b []byte) any {
				d := json.NewDecoder(bytes.NewReader(b))
				d.UseNumber()
				var v any
				if e := d.Decode(&v); e != nil {
					t.Fatal(e)
				}
				return v
			}
			if !reflect.DeepEqual(decode([]byte(tc.raw)), decode(b)) {
				t.Fatalf("wire changed\n%s\n%s", tc.raw, b)
			}
		})
	}
	var update acp.SessionUpdate
	for _, raw := range []string{`{"sessionUpdate":"future","x":1}`, `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}`} {
		if e := json.Unmarshal([]byte(raw), &update); e != nil {
			t.Fatal(e)
		}
	}
	if update.Unrecognized != nil || update.AgentMessageChunk == nil {
		t.Fatal("decoding retained stale variant")
	}
	update.ToolCall = &acp.SessionUpdateToolCall{}
	if _, e := json.Marshal(update); e == nil {
		t.Fatal("ambiguous union emitted")
	}
}
