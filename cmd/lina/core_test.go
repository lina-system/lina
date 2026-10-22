// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3"
)

func TestCoreRefusesUnknownLifecycleVersionBeforeMinting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	controls := filepath.Join(root, "system/core")
	if err := os.MkdirAll(controls, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"main.json":      `{"main_id":"synthetic-main"}`,
		"lifecycle.json": `{"schema":2,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"1"}`,
	} {
		if err := os.WriteFile(filepath.Join(controls, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if code := runCore([]string{"--state-root", root, "--socket", filepath.Join(root, "run/client.sock")}); code != 78 {
		t.Fatalf("unknown lifecycle version exited %d", code)
	}
	if _, err := os.Lstat(filepath.Join(controls, "epoch")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt lifecycle minted epoch: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(controls, "lifecycle.json"))
	if err != nil || string(data) != `{"schema":2,"id":"0123456789abcdef0123456789abcdef","mode":"drain","epoch":"1"}` {
		t.Fatalf("corrupt record changed: %s %v", data, err)
	}
}

func TestCoreRejectsCorruptEngineBeforeBoot(t *testing.T) {
	original := sqlite3.Binary
	sqlite3.Binary = []byte("corrupt engine")
	t.Cleanup(func() { sqlite3.Binary = original })
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if code := runCore([]string{"--state-root", root, "--socket", filepath.Join(root, "client.sock")}); code != 78 {
		t.Fatalf("exit=%d, want configuration error", code)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("boot changed state before verifying the engine: %v %v", entries, err)
	}
}

func TestCorePreflightDoesNotCreateStateOrLoadModelSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	controls := filepath.Join(root, "system/core")
	if err := os.MkdirAll(controls, 0700); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(controls, "main.json")
	if err := os.WriteFile(main, []byte(`{"main_id":"preflight-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINA_RESPONSES_BASE_URL", "invalid provider configuration")
	if code := runCore([]string{"--state-root", root, "--preflight"}); code != 0 {
		t.Fatalf("preflight exit %d", code)
	}
	entries, err := os.ReadDir(controls)
	if err != nil || len(entries) != 1 || entries[0].Name() != "main.json" {
		t.Fatal("preflight wrote controls", entries, err)
	}
	if _, err := os.Stat(filepath.Join(root, "canon")); !os.IsNotExist(err) {
		t.Fatal("preflight created canon", err)
	}
}
