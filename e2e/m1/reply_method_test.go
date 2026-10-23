// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestReplyMethodTypedResultsThroughInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			emitPersonaCall(w, "comparison", "present_result", map[string]any{
				"type": "table", "title": "Comparison", "summary": "A fits.",
				"body": json.RawMessage(`{"columns":[{"name":"Choice","type":"text","unit":""}],"rows":[["A"],["B"]]}`),
			})
		} else {
			emitResponse(w, "Presented.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "typed-comparison", "Compare the options.")
	found := false
	for _, frame := range frames {
		if value, ok := frame.Payload.(protocol.Result); ok && value.Type == protocol.ResultTypeTable {
			found = value.Summary == "A fits." && value.Text != nil
		}
	}
	c.Close()
	h.stopCore(core)
	if !found {
		t.Fatal("present_result did not produce the work method's comparison card in running Core")
	}
}
