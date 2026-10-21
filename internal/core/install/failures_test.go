// SPDX-License-Identifier: Apache-2.0
package install

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceFailurePreservesOnlyAttemptedStart(t *testing.T) {
	for _, phase := range []string{"show", "daemon-reload", "enable"} {
		t.Run(phase, func(t *testing.T) {
			cfg, host := fixture(t)
			host.fail = phase
			if _, err := Run(t.Context(), cfg, host); err == nil {
				t.Fatal("accepted failed service command")
			}
			_, err := os.Stat(cfg.StateRoot)
			if phase == "enable" {
				if err != nil {
					t.Fatal("lost state after a potentially successful start")
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed pre-start install left state: %v", err)
			}
			_, err = os.Stat(filepath.Join(cfg.UnitDir, "lina-core.service"))
			if phase != "enable" && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed pre-start install left a unit")
			}
		})
	}
}

func TestAdapterFilesVerifiedAndCopied(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			cfg, host := fixture(t)
			data, err := os.ReadFile(filepath.Join(cfg.BundleDir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err = json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			files := []artifact{}
			for _, name := range []string{"package.json", "package-lock.json", "adapter.js"} {
				body := []byte("synthetic " + name)
				name = "adapters/claude/" + name
				write(t, filepath.Join(cfg.BundleDir, name), body, 0600)
				files = append(files, artifact{name, int64(len(body)), fmt.Sprintf("%x", sha256.Sum256(body))})
			}
			m["installFiles"] = files
			data, err = json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(cfg.BundleDir, "manifest.json"), data, 0600)
			if corrupt {
				write(t, filepath.Join(cfg.BundleDir, "adapters/claude/package-lock.json"), []byte("changed"), 0600)
			}
			result, err := Run(t.Context(), cfg, host)
			if corrupt {
				if err == nil {
					t.Fatal("accepted changed adapter lockfile")
				}
				if _, err := os.Stat(cfg.StateRoot); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("wrote state before verifying adapter")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				got, err := os.ReadFile(filepath.Join(cfg.StateRoot, "system", file.Name))
				if err != nil || fmt.Sprintf("%x", sha256.Sum256(got)) != file.SHA256 {
					t.Fatal("adapter copy differs from manifest")
				}
			}
			for _, cause := range result.Unavailable {
				if strings.Contains(cause, "Claude") {
					t.Fatal("copied adapter reported absent")
				}
			}
			for _, call := range host.calls {
				if strings.Contains(call, "npm") {
					t.Fatal("installation downloaded the SDK")
				}
			}
		})
	}
}

func TestLinkedParentIsRefused(t *testing.T) {
	cfg, host := fixture(t)
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	cfg.StateRoot = filepath.Join(link, "state")
	if _, err := Run(t.Context(), cfg, host); err == nil {
		t.Fatal("accepted linked parent")
	}
	if _, err := os.Stat(filepath.Join(real, "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote through linked parent")
	}
}

func TestPublicSocketDirectoryIsRefusedBeforeInstall(t *testing.T) {
	cfg, host := fixture(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg.Socket = filepath.Join(dir, "client.sock")
	if _, err := Run(t.Context(), cfg, host); err == nil {
		t.Fatal("accepted a socket directory Core cannot use")
	}
	if _, err := os.Stat(cfg.StateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created unusable installation")
	}
}

func TestServiceUsesAbsoluteUnitPath(t *testing.T) {
	cfg, host := fixture(t)
	if _, err := Run(t.Context(), cfg, host); err != nil {
		t.Fatal(err)
	}
	for _, call := range host.calls {
		if strings.Contains(call, "enable --now ") {
			if !strings.HasSuffix(call, filepath.Join(cfg.UnitDir, "lina-core.service")) {
				t.Fatal("custom unit directory is invisible to systemd enable")
			}
			return
		}
	}
	t.Fatal("did not enable service")
}

func TestOptionalWorkerGetsOwnUserService(t *testing.T) {
	cfg, host := fixture(t)
	body := []byte("synthetic worker")
	write(t, filepath.Join(cfg.BundleDir, "moirai-worker"), body, 0700)
	data, err := os.ReadFile(filepath.Join(cfg.BundleDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["binaries"] = append(m["binaries"].([]any), map[string]any{"name": "moirai-worker", "size": len(body), "sha256": fmt.Sprintf("%x", sha256.Sum256(body))})
	data, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg.BundleDir, "manifest.json"), data, 0600)
	result, err := Run(t.Context(), cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(cfg.UnitDir, "lina-moirai.service"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), filepath.Join(cfg.StateRoot, "system/bin/moirai-worker")) || !strings.Contains(string(unit), "Requires=lina-core.service") {
		t.Fatal("worker service is not tied to Core")
	}
	info, err := os.Stat(filepath.Join(cfg.StateRoot, "system/bin/moirai-worker"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("worker is not executable")
	}
	for _, cause := range result.Unavailable {
		if strings.Contains(cause, "moirai-worker") {
			t.Fatal("included worker reported absent")
		}
	}
	for _, call := range host.calls {
		if strings.Contains(call, "enable --now") && strings.HasSuffix(call, filepath.Join(cfg.UnitDir, "lina-moirai.service")) {
			return
		}
	}
	t.Fatal("optional worker not enabled")
}
