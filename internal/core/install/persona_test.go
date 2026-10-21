// SPDX-License-Identifier: Apache-2.0

package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thisisjun786/lina/persona"
)

func TestInstallReadsPersonaSourceWithoutCopyingMaterial(t *testing.T) {
	for _, available := range []bool{false, true} {
		cfg, host := fixture(t)
		cfg.PersonaSource = t.TempDir()
		if available {
			for _, name := range []string{"core-v1.md", "default-voice-v1.md"} {
				write(t, filepath.Join(cfg.PersonaSource, "characters/lina/persona", name), []byte("synthetic installation fixture"), 0600)
			}
		}
		result, err := Run(t.Context(), cfg, host)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(result.Unavailable, persona.SetupWarning) == available {
			t.Fatal("install did not check both source files")
		}
		data, err := os.ReadFile(filepath.Join(cfg.StateRoot, "system/core/config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var saved map[string]string
		if err := json.Unmarshal(data, &saved); err != nil || saved["persona_source"] != cfg.PersonaSource || len(saved) != 1 {
			t.Fatal("config must retain only the source path")
		}
		for _, path := range []string{"canon", "versions/agents", "materials/lina"} {
			entries, err := os.ReadDir(filepath.Join(cfg.StateRoot, path))
			if err != nil || len(entries) != 0 {
				t.Fatalf("install copied material into %s: %v", path, err)
			}
		}
	}
}
