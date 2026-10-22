// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

var toolNames = []string{"codex", "claude", "antigravity", "devin", "gh", "grok", "node", "bwrap"}

func discover(ctx context.Context, cfg Config, host Host, verified map[string][]string) []Tool {
	tools := make([]Tool, 0, len(toolNames))
	for _, name := range toolNames {
		tool := Tool{Name: name, Status: "missing", VerifiedVersions: append([]string{}, verified[name]...)}
		path := cfg.ToolPaths[name]
		var err error
		if path == "" {
			executable := name
			if name == "antigravity" {
				executable = "agy"
			}
			path, err = host.LookPath(executable)
		}
		if err == nil && filepath.IsAbs(path) {
			tool.Path = path
			probe, cancel := context.WithTimeout(ctx, 3*time.Second)
			out, err := host.Run(probe, path, "--version")
			cancel()
			tool.Status = "unavailable"
			if err == nil && len(out) <= 4096 {
				line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
				line = strings.TrimSpace(line)
				if line != "" && strings.IndexFunc(line, unicode.IsControl) < 0 {
					tool.Version = line
					tool.Status = "unverified"
					if slices.Contains(tool.VerifiedVersions, line) {
						tool.Status = "verified"
					}
				}
			}
		}
		tools = append(tools, tool)
	}
	return tools
}
