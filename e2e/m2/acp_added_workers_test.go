// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"strings"
	"testing"
)

// The shared scenario installs and boots Core, dispatches through the model
// tool, observes unverified readiness/progress, then verifies a committed result
// and the adapter-derived receipt. No installed vendor or credentials are used.
func TestAddedACPWorkersThroughRunningCore(t *testing.T) {
	profiles := `{"version":1,"profiles":[
		{"name":"my-agent","command":"synthetic-agent","args":["acp","literal argument; $HOME"]},
		{"name":"broken","command":"","args":[]},
		{"name":"gemini","command":"must-not-run","args":[]},
		{"name":"codex","command":"must-not-run","args":[]}
	]}`
	addedScript := strings.Replace(acpAgentScript, "const args={gemini:", "const args={'my-agent':['acp','literal argument; $HOME'],gemini:", 1)
	runACPWorkersThroughRunningCore(t, []acpScenario{
		{name: "added", worker: "my-agent", binary: "synthetic-agent", mode: "success", script: addedScript, profiles: profiles,
			startupWarnings: []string{"profile 2: command is required", "profile 3: name is reserved", "profile 4: name is reserved"}},
		{name: "builtin-survives-invalid-profiles", worker: "gemini", binary: "gemini", mode: "success", profiles: profiles,
			startupWarnings: []string{"profile 2: command is required", "profile 3: name is reserved", "profile 4: name is reserved"}},
		{name: "builtin-survives-malformed-file", worker: "gemini", binary: "gemini", mode: "success", profiles: `{"version":1,"profiles":[`,
			startupWarnings: []string{"worker-profiles.json: invalid JSON"}},
	})
}
