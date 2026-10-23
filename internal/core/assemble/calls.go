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

// CondenseResultCall projects one provider item for the model. It returns the
// input unchanged unless the item presents an authored result with a body.
func CondenseResultCall(item provider.Item) provider.Item {
	if !bytes.Contains(item, []byte(`"present_result"`)) {
		return item
	}
	var call map[string]json.RawMessage
	if json.Unmarshal(item, &call) != nil {
		return item
	}
	var kind, name string
	if json.Unmarshal(call["type"], &kind) != nil || kind != "function_call" ||
		json.Unmarshal(call["name"], &name) != nil || name != "present_result" {
		return item
	}
	var arguments string
	if json.Unmarshal(call["arguments"], &arguments) != nil {
		return item
	}
	var presented struct {
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal([]byte(arguments), &presented) != nil || len(presented.Body) == 0 || bytes.Equal(presented.Body, []byte("null")) {
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

// CondenseResultCalls replaces presented result bodies in a loop request with
// their bounded reference. Every other item, including call identity and
// placement, stays byte-for-byte as the loop recorded it.
func CondenseResultCalls(request provider.Request) provider.Request {
	items := request.Items
	copied := false
	for i, item := range request.Items {
		condensed := CondenseResultCall(item)
		if bytes.Equal(condensed, item) {
			continue
		}
		if !copied {
			items = append([]provider.Item(nil), request.Items...)
			copied = true
		}
		items[i] = condensed
	}
	if !copied {
		return request
	}
	request.Items = items
	return request
}
