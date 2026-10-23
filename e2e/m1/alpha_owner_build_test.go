// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Keep the measured product fixed while extending the acceptance harness.
// This opt-in never changes ordinary fixture or foundation builds.
func alphaOwnerInstalledHarness(t *testing.T) *harness {
	t.Helper()
	directory := os.Getenv("LINA_ALPHA_BUILD")
	if directory == "" {
		h, _ := installedConversationHarness(t, "")
		return h
	}
	if !filepath.IsAbs(directory) {
		t.Fatal("LINA_ALPHA_BUILD must be absolute")
	}
	var manifest struct {
		Commit   string `json:"source_commit"`
		Dirty    bool   `json:"source_dirty"`
		Binaries map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"binaries"`
	}
	raw, err := os.ReadFile(filepath.Join(directory, "pinned-binaries.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Dirty || len(manifest.Commit) != 40 {
		t.Fatal("invalid pinned alpha build manifest")
	}
	commands := map[string]string{}
	for _, name := range []string{"lina", "lina-node"} {
		path := filepath.Join(directory, "binaries", name)
		data, err := os.ReadFile(path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != manifest.Binaries[name].SHA256 {
			t.Fatalf("pinned %s digest mismatch", name)
		}
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		settings := map[string]string{}
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
		}
		if settings["vcs.revision"] != manifest.Commit || settings["vcs.modified"] != "false" {
			t.Fatalf("pinned %s is not the clean recorded commit", name)
		}
		commands[name] = path
	}
	h := newHarness(t, commands["lina"])
	bundle := makeInstallBundle(t, h.root, commands["lina"], commands["lina-node"])
	h, _ = installConversationBundle(t, h, bundle, "")
	t.Logf("pinned product commit=%s sha256=%s", manifest.Commit, manifest.Binaries["lina"].SHA256)
	return h
}
