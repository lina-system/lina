// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestExpandToolBindsAgentAndRejectsCallerScope(t *testing.T) {
	calls := 0
	spec, handler := ExpandTool("agent", func(_ context.Context, agent, id string, offset, limit int) (json.RawMessage, error) {
		calls++
		if agent != "agent" || id != "event:fact" || offset != 0 || limit != 50 {
			t.Fatal("untrusted scope")
		}
		return json.RawMessage(`{"events":[{"id":"fact","body":"value 42"}]}`), nil
	})
	for _, raw := range []string{`{"id":"event:fact","agent":"other"}`, `{"id":"event:fact","offset":-1}`, `{"id":"event:fact","limit":1001}`, `{"id":"event:fact"} {}`} {
		if _, err := spec.Schema(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	in, err := spec.Schema(json.RawMessage(`{"id":"event:fact"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := handler(t.Context(), lease.Lease{}, in)
	if err != nil || got.State != tools.Applied || calls != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	_, failed := ExpandTool("agent", func(context.Context, string, string, int, int) (json.RawMessage, error) {
		return nil, errors.New("bytes_unavailable")
	})
	got, err = failed(t.Context(), lease.Lease{}, in)
	if err == nil || got.State != tools.Failed || len(got.Body) != 0 {
		t.Fatal("returned incomplete data as success")
	}
}
