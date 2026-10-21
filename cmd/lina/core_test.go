// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
