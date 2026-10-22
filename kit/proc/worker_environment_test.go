// SPDX-License-Identifier: Apache-2.0
package proc

import (
	"slices"
	"testing"
)

func TestWorkerEnvironmentKeepsOwnSetupWithoutCoreCredential(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "worker-own-key")
	t.Setenv("CODEX_HOME", "/synthetic/user-codex")
	t.Setenv("LINA_RESPONSES_API_KEY", "core-only-key")
	env := WorkerEnvironment()
	for _, entry := range []string{"OPENAI_API_KEY=worker-own-key", "CODEX_HOME=/synthetic/user-codex"} {
		if !slices.Contains(env, entry) {
			t.Fatalf("worker setup omitted: %s", entry)
		}
	}
	if slices.Contains(env, "LINA_RESPONSES_API_KEY=core-only-key") {
		t.Fatal("Core credential reached worker")
	}
}
