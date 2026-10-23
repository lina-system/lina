// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/manifest"
)

type fakeHost struct {
	loaded   bool
	calls    []string
	versions map[string]string
	fail     string
}

func (h *fakeHost) LookPath(name string) (string, error) {
	if name == "systemctl" || h.versions[name] != "" {
		return "/tools/" + name, nil
	}
	return "", os.ErrNotExist
}
func (h *fakeHost) Run(_ context.Context, path string, args ...string) ([]byte, error) {
	call := filepath.Base(path) + " " + strings.Join(args, " ")
	h.calls = append(h.calls, call)
	if h.fail != "" && strings.Contains(call, h.fail) {
		return nil, errors.New("injected command failure")
	}
	if filepath.Base(path) != "systemctl" {
		return []byte(h.versions[filepath.Base(path)]), nil
	}
	if slices.Contains(args, "show") {
		if h.loaded {
			return []byte("loaded\n"), nil
		}
		return []byte("not-found\n"), nil
	}
	return nil, nil
}

func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (Config, *fakeHost) {
	t.Helper()
	// Unix sockets have a fixed path bound independent of the test name.
	base, err := os.MkdirTemp("", "li-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(base); err != nil {
			t.Error(err)
		}
	})
	cfg := Config{StateRoot: filepath.Join(base, "state"), UnitDir: filepath.Join(base, "units"), BundleDir: filepath.Join(base, "bundle")}
	binaries := []map[string]any{}
	for _, name := range []string{"lina", "lina-node"} {
		data := []byte("synthetic " + name)
		write(t, filepath.Join(cfg.BundleDir, name), data, 0700)
		binaries = append(binaries, map[string]any{"name": name, "size": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	m := map[string]any{
		"supportedCombinations": manifest.ClientCombinations(),
		"target":                runtime.GOOS + "/" + runtime.GOARCH,
		"version":               "0.0.1-alpha.dev", "commit": strings.Repeat("a", 40), "date": "2026-10-20", "goToolchain": "go1.27.3", "kitVersion": 1,
		"providers": map[string]any{"chatgpt": map[string]any{"adapter": "responses", "retryCount": 0, "requestFields": []string{"model", "input", "store", "stream"}}},
		"sqlite":    map[string]any{"module": "github.com/ncruces/go-sqlite3", "version": "v0.30.5", "moduleSum": "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=", "sqliteVersion": "3.51.2", "wasmSHA256": strings.Repeat("d", 64), "execution": "wazero", "sqliteVec": nil},
		"binaries":  binaries, "verifiedTools": map[string][]string{"codex": {"codex-cli 1.0"}},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.BundleDir, "manifest.json"), data, 0600)
	return cfg, &fakeHost{versions: map[string]string{"codex": "codex-cli 2.0\n", "cursor-agent": "cursor-agent 1.0\n", "gh": "gh 3.0\n"}}
}

func TestInstallWithoutAccount(t *testing.T) {
	cfg, host := fixture(t)
	result, err := Run(t.Context(), cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	if result.MainID == "" || result.Socket != filepath.Join(cfg.StateRoot, "system/run/client.sock") {
		t.Fatalf("identity: %+v", result)
	}
	for _, name := range []string{"canon", "versions/agents", "versions/records", "materials/user", "materials/lina", "derived", "workspace", "work/clones", "work/tasks", "work/runs", "plugins", "staging/import", "staging/export", "system/core", "system/run", "secrets", "backups"} {
		info, err := os.Lstat(filepath.Join(cfg.StateRoot, name))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("private directory %s: %v %v", name, info, err)
		}
	}
	var id struct {
		MainID string `json:"main_id"`
	}
	data, err := os.ReadFile(filepath.Join(cfg.StateRoot, "system/core/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &id) != nil || id.MainID != result.MainID {
		t.Fatal("identity was not persisted")
	}
	entries, err := os.ReadDir(filepath.Join(cfg.StateRoot, "secrets"))
	if err != nil || len(entries) != 0 {
		t.Fatal("installation created a credential")
	}
	for _, name := range []string{"system/bin/lina", "system/bin/manifest.json", "system/install.json", "system/tools.json"} {
		if _, err := os.Stat(filepath.Join(cfg.StateRoot, name)); err != nil {
			t.Fatal(err)
		}
	}
	unit, err := os.ReadFile(filepath.Join(cfg.UnitDir, "lina-core.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), filepath.Join(cfg.StateRoot, "system/bin/lina")) || !strings.Contains(string(unit), result.Socket) {
		t.Fatal("service does not launch the installed binary and socket")
	}
	if !slices.Contains(host.calls, "systemctl --user enable --now "+filepath.Join(cfg.UnitDir, "lina-core.service")) {
		t.Fatalf("service not registered: %v", host.calls)
	}
	data, err = os.ReadFile(filepath.Join(cfg.StateRoot, "system/tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tools []Tool
	if err := json.Unmarshal(data, &tools); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(tools, func(tool Tool) bool { return tool.Name == "codex" })
	if i < 0 || tools[i].Version != "codex-cli 2.0" || tools[i].Status != "unverified" || !slices.Equal(tools[i].VerifiedVersions, []string{"codex-cli 1.0"}) {
		t.Fatalf("tool evidence: %+v", tools)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if want := []string{"codex", "claude", "antigravity", "cursor-agent", "gemini", "gh", "grok", "node", "bwrap"}; !slices.Equal(names, want) {
		t.Fatalf("installed tool names: got %v, want %v", names, want)
	}
	i = slices.IndexFunc(tools, func(tool Tool) bool { return tool.Name == "cursor-agent" })
	if i < 0 || tools[i].Path != "/tools/cursor-agent" || tools[i].Version != "cursor-agent 1.0" || tools[i].Status != "unverified" {
		t.Fatalf("Cursor tool evidence: %+v", tools)
	}
}

func TestInstallCursorAgentOverride(t *testing.T) {
	cfg, host := fixture(t)
	cfg.ToolPaths = map[string]string{"cursor-agent": "/custom/cursor-cli"}
	host.versions["cursor-cli"] = "cursor-agent 2.0\n"
	result, err := Run(t.Context(), cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(result.Tools, func(tool Tool) bool { return tool.Name == "cursor-agent" })
	if i < 0 || result.Tools[i].Path != "/custom/cursor-cli" || result.Tools[i].Version != "cursor-agent 2.0" || result.Tools[i].Status != "unverified" {
		t.Fatalf("Cursor override evidence: %+v", result.Tools)
	}
}

func TestConflictsDoNotWrite(t *testing.T) {
	for _, conflict := range []string{"foreign unit", "loaded unit", "occupied port", "occupied socket", "existing state", "unit symlink", "unit drop-in"} {
		t.Run(conflict, func(t *testing.T) {
			cfg, host := fixture(t)
			sentinel := ""
			switch conflict {
			case "foreign unit":
				sentinel = filepath.Join(cfg.UnitDir, "lina-core.service")
			case "loaded unit":
				host.loaded = true
			case "occupied port":
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				cfg.ListenAddress = listener.Addr().String()
			case "occupied socket":
				cfg.Socket = filepath.Join(t.TempDir(), "client.sock")
				sentinel = cfg.Socket
			case "existing state":
				sentinel = filepath.Join(cfg.StateRoot, "keep")
			case "unit symlink":
				sentinel = filepath.Join(t.TempDir(), "foreign")
				write(t, sentinel, []byte("keep"), 0600)
				if err := os.MkdirAll(cfg.UnitDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(sentinel, filepath.Join(cfg.UnitDir, "lina-core.service")); err != nil {
					t.Fatal(err)
				}
			case "unit drop-in":
				sentinel = filepath.Join(cfg.UnitDir, "lina-core.service.d/override.conf")
			}
			if sentinel != "" {
				write(t, sentinel, []byte("keep"), 0600)
			}
			if _, err := Run(t.Context(), cfg, host); err == nil {
				t.Fatal("accepted conflict")
			}
			if sentinel != "" {
				data, err := os.ReadFile(sentinel)
				if err != nil || string(data) != "keep" {
					t.Fatal("overwrote foreign data")
				}
			}
			if conflict != "existing state" {
				if _, err := os.Lstat(cfg.StateRoot); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("created state on refusal: %v", err)
				}
			}
			for _, call := range host.calls {
				if strings.Contains(call, "enable") || strings.Contains(call, "daemon-reload") {
					t.Fatalf("mutated services on refusal: %s", call)
				}
			}
		})
	}
}

func TestBadBundleDoesNotWrite(t *testing.T) {
	cfg, host := fixture(t)
	write(t, filepath.Join(cfg.BundleDir, "lina"), []byte("changed"), 0700)
	if _, err := Run(t.Context(), cfg, host); err == nil {
		t.Fatal("accepted changed binary")
	}
	if _, err := os.Lstat(cfg.StateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created state before verifying artifacts")
	}
}

func TestToolFailureDoesNotBlockInstall(t *testing.T) {
	cfg, host := fixture(t)
	host.fail = "codex --version"
	result, err := Run(t.Context(), cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(result.Tools, func(tool Tool) bool { return tool.Name == "codex" })
	if i < 0 || result.Tools[i].Status != "unavailable" {
		t.Fatalf("tool failure: %+v", result.Tools)
	}
}

func TestExistingInstallIsPreserved(t *testing.T) {
	cfg, host := fixture(t)
	first, err := Run(t.Context(), cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(cfg.StateRoot, "system/core/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	host.calls = nil
	if _, err := Run(t.Context(), cfg, host); err == nil {
		t.Fatal("reinstalled existing state")
	}
	after, err := os.ReadFile(filepath.Join(cfg.StateRoot, "system/core/main.json"))
	if err != nil || string(before) != string(after) || first.MainID == "" {
		t.Fatal("changed identity")
	}
	for _, call := range host.calls {
		if strings.Contains(call, "enable") || strings.Contains(call, "restart") {
			t.Fatalf("restarted existing service: %s", call)
		}
	}
}
