// SPDX-License-Identifier: Apache-2.0
package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestInstallVerifiedVersions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fields   map[string]any
		status   string
		versions []string
	}{
		{"build field", map[string]any{"verifiedWorkerVersions": map[string][]string{"codex": {"0.163.0"}}}, "verified", []string{"0.163.0"}},
		{"legacy field", map[string]any{"verifiedTools": map[string][]string{"codex": {"codex-cli 0.163.0"}}}, "verified", []string{"codex-cli 0.163.0"}},
		{"missing list", nil, "unverified", nil},
		{"canonical wins", map[string]any{
			"verifiedWorkerVersions": map[string][]string{"codex": {"0.162.0"}},
			"verifiedTools":          map[string][]string{"codex": {"codex-cli 0.163.0"}},
		}, "unverified", []string{"0.162.0"}},
		{"empty canonical wins", map[string]any{
			"verifiedWorkerVersions": map[string][]string{},
			"verifiedTools":          map[string][]string{"codex": {"codex-cli 0.163.0"}},
		}, "unverified", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, host := fixture(t)
			host.versions["codex"] = "codex-cli 0.163.0\n"
			path := filepath.Join(cfg.BundleDir, "manifest.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			delete(m, "verifiedTools")
			delete(m, "verifiedWorkerVersions")
			for key, value := range tc.fields {
				m[key] = value
			}
			data, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			write(t, path, data, 0600)
			result, err := Run(t.Context(), cfg, host)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(result.Tools, func(tool Tool) bool { return tool.Name == "codex" })
			if i < 0 || result.Tools[i].Status != tc.status || !slices.Equal(result.Tools[i].VerifiedVersions, tc.versions) {
				t.Fatalf("tool inventory: %+v", result.Tools)
			}
		})
	}
}

func TestDiscoverNormalizedCodexVersion(t *testing.T) {
	for _, tc := range []struct {
		name, output, verified, status string
	}{
		{"codex", "codex-cli 0.163.0\n", "0.163.0", "verified"},
		{"codex", "0.163.0\n", "codex-cli 0.163.0", "verified"},
		{"codex", "codex-cli 0.162.0\n", "0.163.0", "unverified"},
		{"codex", "codex-cli 0.163.0-beta.1\n", "0.163.0", "unverified"},
		{"codex", "other-cli 0.163.0\n", "0.163.0", "unverified"},
		{"gh", "gh 0.163.0\n", "0.163.0", "unverified"},
		{"gh", "gh 0.163.0\n", "gh 0.163.0", "verified"},
	} {
		t.Run(tc.name+"/"+tc.output+"/"+tc.verified, func(t *testing.T) {
			host := &fakeHost{versions: map[string]string{tc.name: tc.output}}
			tools := discover(t.Context(), Config{}, host, map[string][]string{tc.name: {tc.verified}})
			i := slices.IndexFunc(tools, func(tool Tool) bool { return tool.Name == tc.name })
			if i < 0 || tools[i].Status != tc.status || tools[i].Version+"\n" != tc.output {
				t.Fatalf("tool inventory: %+v", tools)
			}
		})
	}
}
