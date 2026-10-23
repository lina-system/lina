// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/proc"
)

func TestCoreConfiguredClaudeCredentialDoesNotReachWorkers(t *testing.T) {
	for _, name := range []string{"LINA_RESPONSES_BASE_URL", "LINA_RESPONSES_MODEL", "LINA_RESPONSES_API_KEY"} {
		t.Setenv(name, "")
	}
	t.Setenv("LINA_CLAUDE_NODE", filepath.Join(t.TempDir(), "node"))
	t.Setenv("LINA_CLAUDE_ADAPTER", filepath.Join(t.TempDir(), "adapter.mjs"))
	t.Setenv("LINA_CLAUDE_MODEL", "fixture-model")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "synthetic-core-claude-token")
	t.Setenv("OPENAI_API_KEY", "synthetic-worker-own-key")
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls := filepath.Join(root, "system/core")
	if err := os.MkdirAll(controls, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controls, "main.json"), []byte(`{"main_id":"synthetic-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Stop boot after model configuration, without launching any process.
	if err := os.WriteFile(filepath.Join(controls, "lifecycle.json"), []byte(`{"schema":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := runCore([]string{"--state-root", root, "--socket", filepath.Join(root, "client.sock")}); code != 78 {
		t.Fatalf("invalid lifecycle exit = %d", code)
	}
	for _, env := range [][]string{proc.InheritedEnvironment(), proc.WorkerEnvironment()} {
		for _, entry := range env {
			if strings.HasPrefix(entry, "CLAUDE_CODE_OAUTH_TOKEN=") {
				t.Fatal("configured Core credential remains available to a child")
			}
		}
	}
	if os.Getenv("OPENAI_API_KEY") != "synthetic-worker-own-key" {
		t.Fatal("unrelated worker configuration changed")
	}
}
