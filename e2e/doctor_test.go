// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1Doctor(t *testing.T) {
	data, err := os.ReadFile("m1/doctor.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		RefusalExit int    `json:"refusal_exit"`
		Corruption  string `json:"corruption"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	healthy := newHarness(t, binary)
	defaultRoot := healthy.root
	healthy.root = filepath.Join(defaultRoot, "lina")
	// Installation supplies canon/. A stop immediately after bind can precede
	// Core's sandbox setup, so the fixture must not rely on that later phase.
	if err := os.MkdirAll(filepath.Join(healthy.root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	core := healthy.startCore()
	healthy.connect(core).Close()
	healthy.stopCore(core)
	// Core now initializes its account-capable application schema itself. Do not
	// reopen that database with an older subset of the migrations.
	version, _, err := canon.Inspect(t.Context(), filepath.Join(healthy.root, "canon/canon.sqlite"))
	if err != nil || version == 0 {
		t.Fatal("Core did not initialize canon", err)
	}
	t.Run("default root", func(t *testing.T) {
		before := doctorState(t, healthy.root)
		p := healthy.command("doctor")
		p.command.Env = append(p.command.Env, "XDG_STATE_HOME="+defaultRoot)
		p.start(t)
		p.wait(t, 0)
		if !strings.Contains(p.stdout.String(), "OK") {
			t.Fatal("default root was not diagnosed")
		}
		if after := doctorState(t, healthy.root); !reflect.DeepEqual(before, after) {
			t.Fatal("doctor changed default state")
		}
	})
	for _, args := range [][]string{{"doctor", "--unknown"}, {"doctor", "unexpected"}} {
		p := healthy.command(args...)
		p.start(t)
		p.wait(t, 64)
	}

	for _, name := range []string{"healthy", "fresh install", "corrupt canon", "invalid identity", "invalid epoch", "invalid restart", "invalid sentinel", "unsafe lock", "halted", "recovering", "missing canon", "unknown canon file", "missing root", "missing metadata", "healthy WAL", "WAL"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, binary)
			// Copy only synthetic, stopped Core state. Never touch a real install.
			if err := os.CopyFS(h.root, os.DirFS(healthy.root)); err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(healthy.root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				name, err := filepath.Rel(healthy.root, path)
				if err != nil {
					return err
				}
				return os.Chmod(filepath.Join(h.root, name), info.Mode().Perm())
			}); err != nil {
				t.Fatal(err)
			}
			code, component := 0, ""
			write := func(path, body string) {
				if err := os.WriteFile(filepath.Join(h.root, path), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "corrupt canon":
				write("canon/canon.sqlite", scenario.Corruption)
				code, component = scenario.RefusalExit, "canon"
				failed := h.command("core", "--state-root", h.root, "--socket", h.socket)
				failed.start(t)
				failed.wait(t, scenario.RefusalExit)
			case "invalid identity":
				write("system/core/main.json", `{}`)
				code, component = scenario.RefusalExit, "main.json"
			case "invalid epoch":
				write("system/core/epoch", "invalid")
				code, component = scenario.RefusalExit, "epoch"
			case "invalid restart":
				write("system/core/lifecycle.json", `{"schema":2}`)
				code, component = scenario.RefusalExit, "lifecycle.json"
			case "invalid sentinel":
				write("system/core/run.sentinel", `{"epoch":"1","socket":null}`)
				code, component = scenario.RefusalExit, "run.sentinel"
			case "unsafe lock":
				if err := os.Chmod(filepath.Join(h.root, "system/core/core.lock"), 0644); err != nil {
					t.Fatal(err)
				}
				code, component = scenario.RefusalExit, "core.lock"
			case "halted", "recovering":
				write("system/core/halt.json", `{"schema":1,"epoch":"1","cause":"sqlite_corrupt"}`)
				code, component = scenario.RefusalExit, "halt.json"
				if name == "recovering" {
					// An unused canon lets the production listener serve RECOVERING
					// without trying to initialize conversation stores through a halt.
					if err := os.Remove(filepath.Join(h.root, "canon/canon.sqlite")); err != nil {
						t.Fatal(err)
					}
					// Hold recovery at an explicit boot boundary: otherwise the later
					// sandbox probe can change files while doctor is being checked for writes.
					ctx, cancel := context.WithCancel(h.ctx)
					listening, done := make(chan struct{}), make(chan error, 1)
					go func() {
						done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket,
							Services: boot.Services{Reconcile: func(ctx context.Context, _ boot.Recovery) error { <-ctx.Done(); return nil }},
							AfterStep: func(step boot.Step, _ boot.Snapshot) error {
								if step == boot.SocketsBound {
									close(listening)
								}
								return nil
							},
						})
					}()
					defer func() {
						cancel()
						if err := <-done; err != nil {
							t.Error(err)
						}
					}()
					select {
					case <-listening:
					case <-h.ctx.Done():
						t.Fatal(h.ctx.Err())
					}
					client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", nil)
					if err != nil {
						t.Fatal(err)
					}
					defer client.Close()
					if _, err = client.Input(h.ctx, protocol.Input{}); err != nil {
						t.Fatal(err)
					}
					answer, err := client.Receive(h.ctx)
					if err != nil || answer.Error == nil || answer.Error.Code != protocol.ErrorUnavailable {
						t.Fatalf("expected RECOVERING refusal: %+v %v", answer, err)
					}
				}
			case "missing canon", "fresh install", "unknown canon file":
				if err := os.Remove(filepath.Join(h.root, "canon/canon.sqlite")); err != nil {
					t.Fatal(err)
				}
				if name == "missing canon" {
					write("canon/canon.sqlite-wal", "orphan WAL")
					code, component = scenario.RefusalExit, "canon"
				} else if name == "unknown canon file" {
					write("canon/unrecognized.sqlite", "synthetic unexpected store")
					code, component = scenario.RefusalExit, "canon"
				}
			case "missing root":
				h.root = filepath.Join(h.root, "absent")
				code, component = scenario.RefusalExit, "state root"
			case "WAL", "healthy WAL", "missing metadata":
				db, err := canon.OpenSQLite(t.Context(), filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				// A newer version exists only in committed WAL; ignoring WAL would
				// incorrectly declare this refused store healthy.
				query := "PRAGMA user_version=9999"
				code, component = scenario.RefusalExit, "canon"
				if name == "healthy WAL" {
					query, code = "UPDATE canon_meta SET epoch='2'", 0
				}
				if name == "missing metadata" {
					query = "DROP TABLE canon_meta"
				}
				if _, err := db.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
			}
			before := doctorState(t, h.root)
			p := h.command("doctor", "--state-root", h.root)
			temporary := t.TempDir()
			p.command.Env = append(p.command.Env, "TMPDIR="+temporary)
			// The missing root must not also be the subprocess working directory.
			p.command.Dir = healthy.root
			p.start(t)
			p.wait(t, code)
			if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 0 {
				t.Fatalf("doctor left temporary state: %v %v", entries, err)
			}
			if code == 0 {
				if !strings.Contains(p.stdout.String(), "OK") || p.stderr.Len() != 0 {
					t.Fatalf("healthy diagnosis: stdout=%q stderr=%q", &p.stdout, &p.stderr)
				}
			} else if !strings.Contains(p.stderr.String(), component) || strings.Contains(p.stderr.String(), "usage:") {
				t.Fatalf("diagnosis lacks failed component %s: %q", component, &p.stderr)
			}
			if code != 0 {
				t.Logf("diagnosis: %s", &p.stderr)
			}
			if after := doctorState(t, h.root); !reflect.DeepEqual(before, after) {
				t.Fatal("doctor changed installed state")
			}
		})
	}
}

type doctorFile struct {
	Mode     fs.FileMode
	Size     int64
	Modified int64
	Hash     [32]byte
}

func doctorState(t *testing.T, root string) map[string]doctorFile {
	t.Helper()
	files := map[string]doctorFile{}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return files
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file := doctorFile{Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime().UnixNano()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file.Hash = sha256.Sum256(data)
		}
		files[name] = file
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
