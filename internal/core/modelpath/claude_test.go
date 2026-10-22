// SPDX-License-Identifier: Apache-2.0
package modelpath

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/claudeproc"
)

func TestExplicitClaudeRoute(t *testing.T) {
	values := map[string]string{"LINA_CLAUDE_NODE": "/tools/node", "LINA_CLAUDE_ADAPTER": "/adapter/main.ts", "LINA_CLAUDE_MODEL": "fixture", "CLAUDE_CODE_OAUTH_TOKEN": "synthetic-secret", "ANTHROPIC_API_KEY": "forbidden", "NODE_OPTIONS": "forbidden", "HOME": "/fixture"}
	get := func(key string) string { return values[key] }
	cfg, err := FromEnv(get)
	if err != nil || cfg == nil {
		t.Fatal(err)
	}
	if cfg.Adapter.Class() != provider.AgentRuntime || cfg.Adapter.Provider() != provider.Claude {
		t.Fatal("wrong route")
	}
	a := cfg.Adapter.(*claudeproc.Adapter)
	if strings.Join(a.Environment, " ") != "HOME=/fixture" {
		t.Fatal("unexpected inherited environment")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", cfg, a), "synthetic-secret") {
		t.Fatal("credential exposed by formatting")
	}
	for _, field := range []string{"LINA_CLAUDE_NODE", "LINA_CLAUDE_ADAPTER", "LINA_CLAUDE_MODEL"} {
		previous := values[field]
		values[field] = ""
		if _, err := FromEnv(get); err == nil {
			t.Fatalf("partial route accepted: %s", field)
		}
		values[field] = previous
	}
	values["LINA_RESPONSES_BASE_URL"] = "https://example.invalid/v1"
	values["LINA_RESPONSES_MODEL"] = "fixture"
	values["LINA_RESPONSES_API_KEY"] = "synthetic-key"
	if _, err := FromEnv(get); err == nil {
		t.Fatal("ambiguous routes accepted")
	}
}
