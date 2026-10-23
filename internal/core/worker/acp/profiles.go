// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"strings"

	"github.com/thisisjun786/lina/kit/proc"
)

// Profile describes an installed worker, not a provider or a settings override.
// Empty version probes use initialize.agentInfo.version, or unknown.
type Profile struct {
	Name, Command     string
	Args, Environment []string
	VersionCommand    string
	VersionArgs       []string
	RateLimitKey      string
	VerifiedVersions  []string
	Managed           bool
}

func BuiltinProfiles() []Profile {
	return []Profile{
		{Name: "claude-acp", Managed: true, Command: "claude-agent-acp", VersionCommand: "claude", VersionArgs: []string{"--version"}, RateLimitKey: "_claude/rateLimit"},
		{Name: "cursor", Command: "cursor-agent", Args: []string{"acp"}, VersionCommand: "cursor-agent", VersionArgs: []string{"--version"}},
		{Name: "gemini", Command: "gemini", Args: []string{"--acp"}},
		{Name: "grok", Command: "grok", Args: []string{"--no-auto-update", "agent", "stdio"}, VersionCommand: "grok", VersionArgs: []string{"--version"}},
	}
}

// Installed resolves executables without starting or installing any tool.
// The Claude wrapper is managed by Core and is prepared only after selection.
func Installed() []Profile {
	var profiles []Profile
	for _, p := range BuiltinProfiles() {
		commandName := p.Command
		if p.Managed {
			commandName = "node"
		}
		command, err := proc.LookPath(commandName)
		if err != nil {
			continue
		}
		p.Command = command
		if p.VersionCommand != "" {
			version, err := proc.LookPath(p.VersionCommand)
			if err != nil {
				continue
			}
			p.VersionCommand = version
		}
		if p.Name == "claude-acp" {
			p.Environment = []string{"CLAUDE_CODE_EXECUTABLE=" + p.VersionCommand}
		}
		profiles = append(profiles, p)
	}
	return profiles
}
func environment(base, additions []string) []string {
	if base == nil {
		base = proc.WorkerEnvironment()
	}
	result := make([]string, 0, len(base)+len(additions))
	for _, entry := range append(append([]string(nil), base...), additions...) {
		if !strings.HasPrefix(entry, "LINA_RESPONSES_API_KEY=") {
			result = append(result, entry)
		}
	}
	return result
}
