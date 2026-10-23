// SPDX-License-Identifier: Apache-2.0
package update

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

func TestClaudeACPReleaseComposition(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binaries := []manifest.Binary{}
	artifacts := []manifest.Binary{}
	for _, name := range []string{"lina", "lina-node", "adapters/claude-acp/package.json", "adapters/claude-acp/package-lock.json"} {
		data := []byte("synthetic " + name)
		write(name, data)
		file := manifest.Binary{Name: name, Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}
		if strings.HasPrefix(name, "adapters/") {
			artifacts = append(artifacts, file)
		} else {
			binaries = append(binaries, file)
		}
	}
	m := map[string]any{"version": "0.0.1-alpha.dev", "commit": strings.Repeat("a", 40), "date": "2026-10-20", "goToolchain": "go1.27.3", "kitVersion": 1, "target": runtime.GOOS + "/" + runtime.GOARCH, "supportedCombinations": manifest.ClientCombinations(), "providers": map[string]any{"chatgpt": map[string]any{"adapter": "responses", "retryCount": 0, "requestFields": []string{"model", "input", "store", "stream"}}}, "sqlite": map[string]any{"module": "github.com/ncruces/go-sqlite3", "version": "v0.30.5", "moduleSum": "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=", "sqliteVersion": "3.51.2", "wasmSHA256": strings.Repeat("d", 64), "execution": "wazero", "sqliteVec": nil}, "binaries": binaries, "installFiles": artifacts}
	raw, _ := json.Marshal(m)
	write("manifest.json", raw)
	if _, err := verifyComposition(t.Context(), dir); err != nil {
		t.Fatal("update rejected locked ACP release", err)
	}
	// Installed verification must check these artifacts at system/adapters, not skip them.
	write("bin/manifest.json", raw)
	for _, b := range binaries {
		write("bin/"+b.Name, []byte("synthetic "+b.Name))
	}
	if _, err := verifyInstalledSystem(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	write("adapters/claude-acp/package-lock.json", []byte("corrupt"))
	if _, err := verifyInstalledSystem(t.Context(), dir); err == nil {
		t.Fatal("installed ACP lockfile corruption ignored")
	}
	if _, err := verifyComposition(t.Context(), dir); err == nil {
		t.Fatal("candidate ACP lockfile corruption ignored")
	}
}
