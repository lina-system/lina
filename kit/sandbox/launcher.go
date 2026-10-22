// SPDX-License-Identifier: Apache-2.0
package sandbox

import (
	"errors"
	"os/exec"
	"path/filepath"
)

// Resolve only executables, never a relative path from the current directory.
func resolveLauncher(name string, locations []string, lookup func(string) (string, error)) (string, error) {
	for _, candidate := range append([]string{name}, locations...) {
		if candidate == "" {
			continue
		}
		path, err := lookup(candidate)
		if err == nil && filepath.IsAbs(path) {
			return path, nil
		}
	}
	return "", errors.New("launcher_missing")
}

func (s *Sandbox) launcher() (string, error) {
	if s.config.Launcher != "" {
		return resolveLauncher("", []string{s.config.Launcher}, exec.LookPath)
	}
	return findLauncher()
}

func resolveHostLauncher(goos, arch string, lookup func(string) (string, error)) (string, error) {
	switch goos {
	case "linux":
		if arch != "amd64" && arch != "arm64" {
			return "", errors.New("unsupported_architecture")
		}
		return resolveLauncher("bwrap", []string{"/usr/bin/bwrap", "/bin/bwrap", "/usr/local/bin/bwrap", "/opt/homebrew/bin/bwrap", "/home/linuxbrew/.linuxbrew/bin/bwrap"}, lookup)
	case "darwin":
		return resolveLauncher("", []string{"/usr/bin/sandbox-exec"}, lookup)
	default:
		return "", errors.New("unsupported_platform")
	}
}
