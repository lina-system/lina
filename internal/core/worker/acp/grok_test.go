// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	wire "github.com/thisisjun786/lina/kit/acp"
)

func TestGrokHandshakeAllowlist(t *testing.T) {
	var response wire.InitializeResponse
	err := json.Unmarshal([]byte(`{"protocolVersion":1,"_meta":{"agentVersion":"1.0.68","host":"private-host"},"agentInfo":{"version":"not-the-grok-version"},"authMethods":[{"id":"private-auth","name":"private-account"}],"agentCapabilities":{"_meta":{"host":"private-capability"},"loadSession":true,"sessionCapabilities":{"list":{"_meta":{"host":"private-list"}},"resume":{},"close":{},"delete":{},"additionalDirectories":{}},"promptCapabilities":{"image":true,"audio":false,"embeddedContext":true,"_meta":{"host":"private-prompt"}},"mcpCapabilities":{"http":true,"sse":false,"_meta":{"host":"private-mcp"}},"auth":{"logout":{"_meta":{"host":"private-auth"}}}}}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	got := grokHandshake(response)
	want := &Handshake{Version: "1.0.68", Capabilities: []string{"session/load", "session/list", "session/resume", "session/close", "session/delete", "session/additionalDirectories", "prompt/image", "prompt/embeddedContext", "mcp/http", "auth/logout"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("handshake: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "_meta") {
		t.Fatalf("metadata retained: %s %v", raw, err)
	}
	a := New(Config{})
	a.result.Handshake = got
	copy := a.Result()
	copy.Handshake.Version = "changed"
	copy.Handshake.Capabilities[0] = "changed"
	if !reflect.DeepEqual(a.Result().Handshake, want) {
		t.Fatal("result reader changed the retained handshake")
	}
}

func TestGrokHandshakeUnknownVersion(t *testing.T) {
	for _, raw := range []string{`{}`, `{"agentVersion":null}`, `{"agentVersion":3}`, `{"agentVersion":{}}`, `{"agentVersion":""}`, `{"agentVersion":"1\nprivate"}`, `{"agentVersion":"` + strings.Repeat("x", 129) + `"}`} {
		var meta map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			t.Fatal(err)
		}
		got := grokHandshake(wire.InitializeResponse{Meta: &meta, AgentInfo: &wire.Implementation{Version: "not-grok"}})
		if got.Version != "unknown" || len(got.Capabilities) != 0 {
			t.Fatalf("invalid metadata claimed a version: %+v", got)
		}
	}
	if got := grokHandshake(wire.InitializeResponse{}); got.Version != "unknown" || got.Capabilities == nil {
		t.Fatalf("absent handshake: %+v", got)
	}
}
