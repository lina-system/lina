// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

var toolNames = []string{"codex", "claude", "antigravity", "cursor-agent", "gemini", "gh", "grok", "node", "bwrap"}

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
			out, err := probeVersion(ctx, host, name, path)
			tool.Status = "unavailable"
			if err == nil && len(out) <= 4096 {
				line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
				line = strings.TrimSpace(line)
				if line != "" && strings.IndexFunc(line, unicode.IsControl) < 0 {
					tool.Version = line
					tool.Status = "unverified"
					if slices.ContainsFunc(tool.VerifiedVersions, func(version string) bool {
						return versionForComparison(name, version) == versionForComparison(name, line)
					}) {
						tool.Status = "verified"
					}
				}
			}
		}
		tools = append(tools, tool)
	}
	return tools
}

// Codex prints a label before its version; the build records bare versions.
// Keep the original output in the inventory and normalize only for comparison.
func versionForComparison(name, version string) string {
	if name == "codex" {
		return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "codex-cli "))
	}
	return version
}

// ACP discovery negotiates the same protocol as dispatch, but never creates a
// session. Hosts without a protocol probe cannot claim a usable Gemini tool.
func probeVersion(ctx context.Context, host Host, name, path string) ([]byte, error) {
	if name == "gemini" {
		probe, ok := host.(interface {
			ACPVersion(context.Context, string, ...string) (string, error)
		})
		if !ok {
			return nil, errors.New("ACP version probe unavailable")
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		version, err := probe.ACPVersion(ctx, path, "--acp")
		return []byte(version), err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return host.Run(ctx, path, "--version")
}
