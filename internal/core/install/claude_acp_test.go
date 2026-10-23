// SPDX-License-Identifier: Apache-2.0
package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeACPLockRejectsUntrustedInputs(t *testing.T) {
	raw, err := os.ReadFile("../../../adapters/claude-acp/package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := readNPMLock(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"source", "integrity", "path", "link"} {
		t.Run(change, func(t *testing.T) {
			var altered npmLock
			if err := json.Unmarshal(raw, &altered); err != nil {
				t.Fatal(err)
			}
			name := "node_modules/@agentclientprotocol/claude-agent-acp"
			p := altered.Packages[name]
			switch change {
			case "source":
				p.Resolved = "https://example.invalid/pkg.tgz"
			case "integrity":
				p.Integrity = "sha1-no"
			case "path":
				name = "node_modules/../../escape"
			case "link":
				p.Link = true
			}
			altered.Packages[name] = p
			data, _ := json.Marshal(altered)
			if _, err := readNPMLock(data); err == nil {
				t.Fatal("accepted", change)
			}
		})
	}
	if len(lock.Packages) < 2 {
		t.Fatal("empty release lock")
	}
}

func TestClaudeACPReuseRejectsChangedFilesAndOptionalPackages(t *testing.T) {
	for _, change := range []string{"entry", "extra", "optional", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, claudeACPEntry)
			if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(entry, []byte("pinned code"), 0600); err != nil {
				t.Fatal(err)
			}
			lock := npmLock{Packages: map[string]npmPackage{"node_modules/optional-claude": {Optional: true}}}
			files, err := npmTree(root)
			if err != nil {
				t.Fatal(err)
			}
			seal, _ := json.Marshal(files)
			if err := os.WriteFile(filepath.Join(root, ".lina-integrity.json"), seal, 0600); err != nil {
				t.Fatal(err)
			}
			if err := verifyNPMTree(root, lock); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "entry":
				err = os.WriteFile(entry, []byte("modified"), 0600)
			case "extra":
				err = os.WriteFile(filepath.Join(root, "extra"), []byte("extra"), 0600)
			case "optional":
				err = os.Mkdir(filepath.Join(root, "node_modules/optional-claude"), 0700)
			case "symlink":
				err = os.Symlink(t.TempDir(), filepath.Join(root, "outside"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyNPMTree(root, lock); err == nil {
				t.Fatal("reused changed installation", change)
			}
		})
	}
}

func TestClaudeACPBundleMissingPairFails(t *testing.T) {
	cfg, _ := fixture(t)
	data, err := os.ReadFile(filepath.Join(cfg.BundleDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	m["installFiles"] = []artifact{{"adapters/claude-acp/extra.js", 1, strings.Repeat("a", 64)}}
	data, _ = json.Marshal(m)
	write(t, filepath.Join(cfg.BundleDir, "manifest.json"), data, 0600)
	if _, err := readBundle(cfg.BundleDir); err == nil {
		t.Fatal("accepted ACP code outside manifest/lockfile")
	}
}
