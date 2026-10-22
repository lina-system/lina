// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/manifest"
)

func TestM2OldCorePreservesNewerStore(t *testing.T) {
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	h.connect(core).Close()
	h.stopCore(core)
	db, err := canon.OpenSQLite(t.Context(), filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version=9999"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.root, "system/core/core.lock")); err != nil {
		t.Fatal(err)
	}
	before := doctorState(t, h.root)
	p := h.command("core", "--state-root", h.root, "--socket", h.socket)
	p.start(t)
	p.wait(t, 78)
	if after := doctorState(t, h.root); !reflect.DeepEqual(before, after) {
		t.Fatal("old Core changed newer installed state")
	}
}

func TestM2UpdatePreflight(t *testing.T) {
	var scenario struct {
		RefusalExit int      `json:"refusal_exit"`
		NewerSchema int      `json:"newer_schema"`
		Cases       []string `json:"cases"`
	}
	data, err := os.ReadFile("../m2/update-preflight.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	healthy := newHarness(t, binary)
	core := healthy.startCore()
	healthy.connect(core).Close()
	healthy.stopCore(core)
	for _, name := range scenario.Cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, binary)
			if err := os.CopyFS(h.root, os.DirFS(healthy.root)); err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(h.root, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return os.Chmod(path, 0700)
				}
				return os.Chmod(path, 0600)
			}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.root, "canon/canon.sqlite")
			code := 0
			rows := manifest.ClientCombinations()
			switch name {
			case "newer", "newer WAL", "missing WAL index", "missing table":
				db, err := canon.OpenSQLite(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				query := fmt.Sprintf("PRAGMA user_version=%d", scenario.NewerSchema)
				code = scenario.RefusalExit
				if name == "missing table" {
					query = "DROP TABLE turn_runs"
				}
				if name == "missing WAL index" {
					query, code = "UPDATE canon_meta SET epoch='2'", 0
				}
				if _, err := db.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
				if name == "newer" {
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if name == "missing WAL index" {
					if err := os.Remove(path + "-shm"); err != nil {
						t.Fatal(err)
					}
				}
			case "older", "fresh":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if name == "older" {
					controls, err := ctlfiles.Open(h.root)
					if err != nil {
						t.Fatal(err)
					}
					if err := controls.Acquire(); err != nil {
						t.Fatal(err)
					}
					db, err := canon.Open(t.Context(), canon.Config{Path: path, Epoch: 1, Controls: controls, Migrations: canon.Migrations()[:14]})
					if err != nil {
						t.Fatal(err)
					}
					if err := db.Close(t.Context()); err != nil {
						t.Fatal(err)
					}
					if err := controls.Close(); err != nil {
						t.Fatal(err)
					}
				}
			case "unsupported peers":
				rows, code = []manifest.Combination{}, scenario.RefusalExit
			case "unimplemented peers":
				rows[0].ProtocolVersion, code = "99.0", scenario.RefusalExit
			case "corrupt":
				if err := os.WriteFile(path, []byte("synthetic corrupt database"), 0600); err != nil {
					t.Fatal(err)
				}
				code = scenario.RefusalExit
			}
			metadata := filepath.Join(t.TempDir(), "manifest.json")
			data, err := json.Marshal(map[string]any{"supportedCombinations": rows})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metadata, data, 0600); err != nil {
				t.Fatal(err)
			}
			// Even a held Core lock must not block preflight. Do not create it in
			// the candidate process, and compare every source byte and metadata.
			controls, err := ctlfiles.Open(h.root)
			if err != nil {
				t.Fatal(err)
			}
			if err := controls.Acquire(); err != nil {
				t.Fatal(err)
			}
			before := doctorState(t, h.root)
			args := []string{"core", "--preflight", "--state-root", h.root, "--manifest", metadata}
			if name == "unimplemented peers" {
				args = append(args, "--required-manifest", metadata)
			}
			p := h.command(args...)
			p.start(t)
			p.wait(t, code)
			if after := doctorState(t, h.root); !reflect.DeepEqual(before, after) {
				t.Fatal("preflight changed installed state")
			}
			if err := controls.Close(); err != nil {
				t.Fatal(err)
			}
			if name == "newer" || name == "newer WAL" {
				// Normal startup must reject a newer schema before even minting
				// an epoch, replacing the sentinel or creating a lock file.
				if err := os.Remove(filepath.Join(h.root, "system/core/core.lock")); err != nil {
					t.Fatal(err)
				}
				before := doctorState(t, h.root)
				p := h.command("core", "--state-root", h.root, "--socket", h.socket)
				p.start(t)
				p.wait(t, scenario.RefusalExit)
				if after := doctorState(t, h.root); !reflect.DeepEqual(before, after) {
					t.Fatal("old Core changed newer installed state")
				}
			}
		})
	}
}
