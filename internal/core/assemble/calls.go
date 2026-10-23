// SPDX-License-Identifier: Apache-2.0
package assemble

import (
	"bytes"
	"encoding/json"

	"github.com/thisisjun786/lina/kit/provider"
)

// A presented result body stays in the ledger. The request keeps the call for
// pairing and ordering, but replaces the body with this bounded reference; the
// paired tool output names the result, its revision and its title.
const resultCallReference = `{"note":"result body stored; the tool result names its id and revision"}`

// storedResults returns the call ids whose paired tool output reports a
// completed presentation. Only those calls have a stored body to refer to; a
// refused, interrupted or unreceipted call keeps its arguments so the model can
// correct what it sent.
func storedResults(items []provider.Item) map[string]bool {
	stored := map[string]bool{}
	for _, item := range items {
		if !bytes.Contains(item, []byte("call_output")) {
			continue
		}
		var output struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		if json.Unmarshal(item, &output) != nil || output.CallID == "" ||
			(output.Type != "function_call_output" && output.Type != "custom_tool_call_output") {
			continue
		}
		var receipt struct{ Code string }
		if json.Unmarshal([]byte(output.Output), &receipt) == nil && receipt.Code == "ok" {
			stored[output.CallID] = true
		}
	}
	return stored
}

// condenseResultCall projects one provider item for the model. It returns the
// input unchanged unless the item presents an authored result whose call
// completed.
func condenseResultCall(item provider.Item, stored map[string]bool) provider.Item {
	if !bytes.Contains(item, []byte("present_result")) {
		return item
	}
	var call map[string]json.RawMessage
	if json.Unmarshal(item, &call) != nil {
		return item
	}
	var kind, name, callID string
	if json.Unmarshal(call["type"], &kind) != nil || kind != "function_call" ||
		json.Unmarshal(call["name"], &name) != nil || name != "present_result" ||
		json.Unmarshal(call["call_id"], &callID) != nil || !stored[callID] {
		return item
	}
	var arguments string
	if json.Unmarshal(call["arguments"], &arguments) != nil {
		return item
	}
	var presented struct {
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal([]byte(arguments), &presented) != nil ||
		len(presented.Body) == 0 || bytes.Equal(presented.Body, []byte("null")) {
		return item
	}
	reference, err := json.Marshal(resultCallReference)
	if err != nil {
		return item
	}
	call["arguments"] = reference
	condensed, err := json.Marshal(call)
	if err != nil {
		return item
	}
	return condensed
}

// CondenseResultCalls replaces presented result bodies in a request with their
// bounded reference. Every other item, including call identity and placement,
// stays byte-for-byte as the loop recorded it.
func CondenseResultCalls(request provider.Request) provider.Request {
	items, changed := CondenseResultItems(request.Items)
	if !changed {
		return request
	}
	request.Items = items
	return request
}

// CondenseResultItems projects a run of provider items together, so a call is
// matched with its paired output before its body is replaced.
func CondenseResultItems(items []provider.Item) ([]provider.Item, bool) {
	if len(items) == 0 {
		return items, false
	}
	stored := storedResults(items)
	projected := items
	copied := false
	for i, item := range items {
		condensed := condenseResultCall(item, stored)
		if bytes.Equal(condensed, item) {
			continue
		}
		if !copied {
			projected = append([]provider.Item(nil), items...)
			copied = true
		}
		projected[i] = condensed
	}
	return projected, copied
}
