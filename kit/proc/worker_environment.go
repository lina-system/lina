// SPDX-License-Identifier: Apache-2.0
package proc

import (
	"os"
	"strings"
)

// WorkerEnvironment captures the person's installed coding-tool setup without
// imposing Core's tool-sandbox policy. Own provider credentials, CODEX_HOME,
// PATH and integrations remain intact; LINA's dedicated model secret does not.
// Core never injects credentials from its vault into a worker environment.
func WorkerEnvironment() []string {
	env := make([]string, 0)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LINA_RESPONSES_API_KEY=") || strings.HasPrefix(entry, "LINA_CLAUDE_API_KEY=") {
			continue
		}
		env = append(env, entry)
	}
	return env
}
