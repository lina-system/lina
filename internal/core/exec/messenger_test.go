// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestMessengerChatsBindsIdentity(t *testing.T) {
	calls := 0
	spec, handler := MessengerChatsTool("bound-agent", func(_ context.Context, q MessengerChatQuery) (json.RawMessage, error) {
		calls++
		if q.Platform != "discord" || q.Limit != 20 {
			t.Fatalf("query: %+v", q)
		}
		return json.RawMessage(`{"results":[]}`), nil
	})
	for _, input := range []string{`{"person":"other"}`, `{"agent":"other"}`, `{"limit":101}`, `{"limit":-1}`} {
		if _, err := spec.Schema(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	in, err := spec.Schema(json.RawMessage(`{"platform":"discord"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Target != "memory/bound-agent" {
		t.Fatalf("target=%q", in.Target)
	}
	result, err := handler(t.Context(), lease.Lease{}, in)
	if err != nil || result.State != tools.Applied || calls != 1 {
		t.Fatalf("result=%+v calls=%d error=%v", result, calls, err)
	}
}
