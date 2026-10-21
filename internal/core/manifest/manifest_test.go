// SPDX-License-Identifier: Apache-2.0
package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validManifest() map[string]any {
	return map[string]any{
		"version": "0.0.1-alpha.dev", "commit": strings.Repeat("a", 40),
		"date": "2026-10-20", "goToolchain": "go1.27.3", "kitVersion": 1,
		"sqlite": map[string]any{
			"module": "github.com/ncruces/go-sqlite3", "version": "v0.30.5",
			"moduleSum":     "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=",
			"sqliteVersion": "3.51.2", "wasmSHA256": strings.Repeat("d", 64),
			"execution": "wazero", "sqliteVec": nil,
		},
		"binaries": []any{
			map[string]any{"name": "lina", "size": 100, "sha256": strings.Repeat("b", 64)},
			map[string]any{"name": "lina-node", "size": 200, "sha256": strings.Repeat("c", 64)},
		},
	}
}

func writeManifest(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRead(t *testing.T) {
	for _, version := range []string{"0.0.1-alpha.dev", "0.0.1-alpha.20261020.1"} {
		value := validManifest()
		value["version"] = version
		value["future"] = map[string]any{"enabled": true}
		value["binaries"].([]any)[0].(map[string]any)["future"] = true
		got, err := Read(writeManifest(t, encoded(t, value)))
		if err != nil {
			t.Fatal(err)
		}
		if got.Version != version || got.Commit != value["commit"] || got.Date != value["date"] ||
			got.GoToolchain != value["goToolchain"] || got.KitVersion != 1 || len(got.Binaries) != 2 ||
			got.SQLite.Module != "github.com/ncruces/go-sqlite3" || got.SQLite.SQLiteVec != nil ||
			got.Binaries[0].Name != "lina" || got.Binaries[0].Size != 100 || got.Binaries[0].SHA256 != strings.Repeat("b", 64) {
			t.Fatalf("unexpected manifest: %+v", got)
		}
	}
}

func TestReadRejectsInvalidFields(t *testing.T) {
	for _, field := range []string{"version", "commit", "date", "goToolchain", "kitVersion", "binaries", "sqlite"} {
		t.Run("missing "+field, func(t *testing.T) {
			value := validManifest()
			delete(value, field)
			if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
				t.Fatal("accepted missing field")
			}
		})
	}
	for field, invalid := range map[string][]any{
		"version":     {"", "v0.0.1-alpha.20261020.1", "0.0.1-alpha.20260230.1", "0.0.1-alpha.20261020.0", "1.0.0", nil},
		"commit":      {"abcdef0", strings.Repeat("z", 40), "", nil},
		"date":        {"2026-02-30", "2026-1-1", "2026-10-20T00:00:00Z", nil},
		"goToolchain": {"go1.27", "1.27.3", "go1.27.3-custom", nil},
		"kitVersion":  {0, -1, 1.5, "1", 9007199254740992, nil},
		"binaries":    {nil, []any{}, "lina"},
	} {
		t.Run(field, func(t *testing.T) {
			for _, bad := range invalid {
				value := validManifest()
				value[field] = bad
				if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
					t.Errorf("accepted %s=%v", field, bad)
				}
			}
		})
	}
}

func TestReadRejectsInvalidSQLite(t *testing.T) {
	for field, invalid := range map[string][]any{
		"module":        {nil, "", "other/sqlite"},
		"version":       {nil, "", "latest", "v1.0"},
		"moduleSum":     {nil, "", "h1:not-a-digest"},
		"sqliteVersion": {nil, "", "3.51"},
		"wasmSHA256":    {nil, "", strings.Repeat("x", 64)},
		"execution":     {nil, "", "cgo"},
		"sqliteVec":     {map[string]any{}, map[string]any{"version": "v0.1.6", "sha256": "invalid"}},
	} {
		for _, bad := range invalid {
			value := validManifest()
			value["sqlite"].(map[string]any)[field] = bad
			if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
				t.Errorf("accepted SQLite %s=%v", field, bad)
			}
		}
	}
}

func TestReadSQLiteVectorEngine(t *testing.T) {
	value := validManifest()
	value["sqlite"].(map[string]any)["sqliteVec"] = map[string]any{"version": "v0.1.6", "sha256": strings.Repeat("d", 64)}
	got, err := Read(writeManifest(t, encoded(t, value)))
	if err != nil || got.SQLite.SQLiteVec == nil || got.SQLite.SQLiteVec.Version != "v0.1.6" {
		t.Fatalf("%+v %v", got.SQLite, err)
	}
}

func TestReadRejectsInvalidBinaries(t *testing.T) {
	for field, invalid := range map[string][]any{
		"name":   {"", "../lina", "/lina", `dir\lina`, "lina-node", nil},
		"size":   {0, -1, 1.5, "100", nil},
		"sha256": {"", strings.Repeat("a", 63), strings.Repeat("z", 64), nil},
	} {
		for _, bad := range invalid {
			value := validManifest()
			value["binaries"].([]any)[0].(map[string]any)[field] = bad
			if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
				t.Errorf("accepted binary %s=%v", field, bad)
			}
		}
	}
	value := validManifest()
	value["binaries"] = value["binaries"].([]any)[:1]
	if _, err := Read(writeManifest(t, encoded(t, value))); err == nil {
		t.Fatal("accepted missing lina-node")
	}
}

func TestReadWindowsNamesAndSHA256Commit(t *testing.T) {
	value := validManifest()
	value["commit"] = strings.Repeat("a", 64)
	for _, entry := range value["binaries"].([]any) {
		binary := entry.(map[string]any)
		binary["name"] = binary["name"].(string) + ".exe"
	}
	if _, err := Read(writeManifest(t, encoded(t, value))); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBinaryPlatforms(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		valid bool
	}{
		{"unix", []string{"lina", "lina-node"}, true},
		{"windows", []string{"lina.exe", "lina-node.exe"}, true},
		{"unix with windows lina", []string{"lina", "lina-node", "lina.exe"}, false},
		{"unix with windows node", []string{"lina", "lina-node", "lina-node.exe"}, false},
		{"windows with unix lina", []string{"lina.exe", "lina-node.exe", "lina"}, false},
		{"windows with unix node", []string{"lina.exe", "lina-node.exe", "lina-node"}, false},
		{"both pairs", []string{"lina", "lina-node", "lina.exe", "lina-node.exe"}, false},
		{"mixed pair", []string{"lina", "lina-node.exe"}, false},
		{"reverse mixed pair", []string{"lina.exe", "lina-node"}, false},
		{"future artifacts with unix", []string{"lina", "lina-node", "lina-helper.exe", "helper"}, true},
		{"future artifacts with windows", []string{"lina.exe", "lina-node.exe", "lina-helper", "helper.exe"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m Manifest
			if err := json.Unmarshal(encoded(t, validManifest()), &m); err != nil {
				t.Fatal(err)
			}
			m.Binaries = nil
			for _, name := range tc.names {
				m.Binaries = append(m.Binaries, Binary{Name: name, Size: 100, SHA256: strings.Repeat("b", 64)})
			}
			if err := m.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate binaries %v: got %v, want valid=%v", tc.names, err, tc.valid)
			}
		})
	}
}

func TestReadRejectsInvalidJSONAndMissingFile(t *testing.T) {
	for _, data := range []string{"", "{", "null", "[]", "{}", string(encoded(t, validManifest())) + "\n{}"} {
		if _, err := Read(writeManifest(t, []byte(data))); err == nil {
			t.Errorf("accepted %q", data)
		}
	}
	if _, err := Read(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted missing file")
	}
}
