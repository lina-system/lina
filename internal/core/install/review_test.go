// SPDX-License-Identifier: Apache-2.0
package install

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketCannotUseInstallerOrCorePaths(t *testing.T) {
	for _, name := range []string{".", "system", "system/core", "system/core/main.json", "system/core/core.lock", "system/bin/lina", "system/tools.json", "canon/client.sock", "unit", "unit directory", "enablement"} {
		t.Run(name, func(t *testing.T) {
			cfg, host := fixture(t)
			switch name {
			case "unit":
				cfg.Socket = filepath.Join(cfg.UnitDir, "lina-core.service")
			case "unit directory":
				cfg.Socket = cfg.UnitDir
			case "enablement":
				cfg.Socket = filepath.Join(cfg.UnitDir, "default.target.wants/lina-core.service")
			default:
				cfg.Socket = filepath.Join(cfg.StateRoot, name)
			}
			if _, err := Run(t.Context(), cfg, host); err == nil {
				t.Fatal("accepted a socket that conflicts with owned installation paths")
			}
			if _, err := os.Lstat(cfg.StateRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("created state before rejecting a reserved socket")
			}
			for _, call := range host.calls {
				if strings.Contains(call, "enable") || strings.Contains(call, "daemon-reload") {
					t.Fatal("changed service state for a reserved socket")
				}
			}
		})
	}
}

func TestAdapterTraversalIsRejectedBeforeWrites(t *testing.T) {
	for _, name := range []string{"adapters/claude/../../../outside", "adapters/claude/../../outside", "adapters/claude/nested/../../outside", "adapters/claude/../claude/package.json", "/adapters/claude/package.json", "adapters/claude/..\\..\\outside", "adapters/claude//package.json"} {
		t.Run(name, func(t *testing.T) {
			cfg, host := fixture(t)
			data, err := os.ReadFile(filepath.Join(cfg.BundleDir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err = json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["installFiles"] = []artifact{{Name: name, Size: 1, SHA256: strings.Repeat("a", 64)}}
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(cfg.BundleDir, "manifest.json"), data, 0600)
			if _, err := Run(t.Context(), cfg, host); err == nil {
				t.Fatal("accepted adapter traversal")
			}
			if _, err := os.Lstat(cfg.StateRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrote state for a rejected adapter name")
			}
		})
	}
}
