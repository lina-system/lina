// SPDX-License-Identifier: Apache-2.0
package assemble_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/kit/provider"
)

// The loop hands Core its raw items. The request keeps one item for every
// recorded one, in place, with only the presented body replaced.
func TestCondenseResultCallsKeepsRequestShape(t *testing.T) {
	items := []provider.Item{
		[]byte(`{"type":"reasoning","encrypted_content":"opaque"}`),
		[]byte(`{"type":"function_call","call_id":"present","name":"present_result","arguments":"{\"type\":\"plan_view\",\"body\":{\"entries\":[{\"id\":\"step\"}]}}"}`),
		[]byte(`{"type":"function_call","call_id":"read","name":"read_result","arguments":"{\"result_id\":\"result:1\"}"}`),
		[]byte(`{"type":"function_call_output","call_id":"present","output":"{\"code\":\"ok\"}"}`),
	}
	request := provider.Request{Model: "fixture", Items: items, Tools: []provider.ToolDecl{[]byte(`{"type":"function","name":"present_result"}`)}}
	out := assemble.CondenseResultCalls(request)
	if out.Model != request.Model || len(out.Items) != len(items) || len(out.Tools) != 1 || !bytes.Equal(out.Tools[0], request.Tools[0]) {
		t.Fatalf("request shape changed: %+v", out)
	}
	for _, i := range []int{0, 2, 3} {
		if !bytes.Equal(out.Items[i], items[i]) {
			t.Fatalf("item %d changed: %s", i, out.Items[i])
		}
	}
	var call struct {
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(out.Items[1], &call); err != nil {
		t.Fatal(err)
	}
	if call.CallID != "present" || call.Name != "present_result" || len(call.Arguments) > 256 || bytes.Contains([]byte(call.Arguments), []byte("step")) {
		t.Fatalf("presented call not projected: %+v", call)
	}
	if !bytes.Contains(items[1], []byte("step")) {
		t.Fatal("the recorded call was mutated")
	}
	if again := assemble.CondenseResultCalls(out); !bytes.Equal(again.Items[1], out.Items[1]) {
		t.Fatalf("projection is not stable: %s", again.Items[1])
	}
}
