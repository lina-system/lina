// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinCommandsPreserveWorkerSetup(t *testing.T) {
	want := map[string][]string{
		"claude-acp": {"claude-agent-acp"},
		"cursor":     {"cursor-agent", "acp"},
		"gemini":     {"gemini", "--acp"},
		"grok":       {"grok", "--no-auto-update", "agent", "stdio"},
	}
	for _, p := range BuiltinProfiles() {
		if !reflect.DeepEqual(append([]string{p.Command}, p.Args...), want[p.Name]) {
			t.Fatalf("unexpected invocation: %+v", p)
		}
		delete(want, p.Name)
		if p.Name == "claude-acp" && p.VersionCommand != "claude" {
			t.Fatal("must probe the person's Claude installation")
		}
	}
	if len(want) > 0 {
		t.Fatal("missing built-in profiles", want)
	}
}
func TestEnvironmentFiltersProfileCredentialToo(t *testing.T) {
	got := environment([]string{"PATH=/bin", "LINA_RESPONSES_API_KEY=core", "PROVIDER_KEY=own"}, []string{"LINA_RESPONSES_API_KEY=override", "CLAUDE_CODE_EXECUTABLE=/tools/claude"})
	for _, s := range got {
		if strings.HasPrefix(s, "LINA_RESPONSES_API_KEY=") {
			t.Fatal("credential leaked")
		}
	}
	if !slices.Contains(got, "PROVIDER_KEY=own") || !slices.Contains(got, "CLAUDE_CODE_EXECUTABLE=/tools/claude") {
		t.Fatal("worker configuration lost")
	}
}
