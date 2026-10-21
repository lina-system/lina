// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"encoding/json"
	"testing"
)

func TestCommandPrefixAndNetworkScopeAreTrusted(t *testing.T) {
	prefix := []string{"fetch", "read"}
	s, err := CommandSpec(CommandPolicy{Name: "exec_command", Target: "workspace", Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	prefix[0] = "other"
	for _, raw := range []string{`{"argv":["other","read"],"network":true}`, `{"argv":["fetch","read"],"prefix":["fetch"]}`, `{"argv":["fetch","read"],"network":true,"outward":false}`, `{"argv":["fetch","read"]} {}`} {
		if _, err := s.Schema(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	a, err := s.Schema(json.RawMessage(`{"argv":["fetch","read","one"],"network":true}`))
	if err != nil || !a.Network || a.Target != "workspace" {
		t.Fatalf("input: %+v %v", a, err)
	}
	b, err := s.Schema(json.RawMessage(`{"argv":["fetch","read","two"],"network":true}`))
	if err != nil || a.ScopeKey != b.ScopeKey {
		t.Fatalf("scope: %+v %v", b, err)
	}
	other, _ := CommandSpec(CommandPolicy{Name: "exec_command", Target: "workspace", Prefix: []string{"fetch", "write"}})
	c, err := other.Schema(json.RawMessage(`{"argv":["fetch","write"],"network":true}`))
	if err != nil || c.ScopeKey == a.ScopeKey {
		t.Fatalf("prefix scope collided: %+v %v", c, err)
	}
}
