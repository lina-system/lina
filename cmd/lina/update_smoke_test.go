// SPDX-License-Identifier: Apache-2.0
package main

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/proc"
)

func TestUpdateCoreEnvironmentCredentialBoundary(t *testing.T) {
	const token = "synthetic-update-token=opaque"
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", token)
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "CLAUDE_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "LD_PRELOAD"} {
		t.Setenv(name, "synthetic-forbidden-value")
	}
	t.Setenv("LINA_CLAUDE_MODEL", "fixture-model")
	count := 0
	for _, entry := range updateCoreEnvironment() {
		if strings.Contains(entry, "synthetic-forbidden-value") {
			t.Fatal("unrelated credential or injection setting reached Core")
		}
		if strings.HasPrefix(entry, "CLAUDE_CODE_OAUTH_TOKEN=") {
			if entry != "CLAUDE_CODE_OAUTH_TOKEN="+token {
				t.Fatal("configured credential changed")
			}
			count++
		}
	}
	if count != 1 {
		t.Fatalf("credential occurrences = %d, want 1", count)
	}
	for _, entry := range proc.InheritedEnvironment() {
		if strings.Contains(entry, token) || strings.Contains(entry, "synthetic-forbidden-value") {
			t.Fatal("Core exception widened the tool environment")
		}
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	for _, entry := range updateCoreEnvironment() {
		if strings.HasPrefix(entry, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatal("empty credential was forwarded")
		}
	}
}
