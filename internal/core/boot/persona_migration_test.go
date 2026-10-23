// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/skills"
	builtin "github.com/thisisjun786/lina/skills"
)

func TestPersonaSkillMigrationPreservesExistingAgent(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := boot.ApplicationMigrations()
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations[:len(migrations)-1], NoSync: true}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store, err := agents.Open(root, db, agents.NoSync())
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person", Layers: agents.Layers{UserVoice: "fixture voice"}})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations = migrations
	cfg.Epoch++
	db, err = canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err = agents.Open(root, db, agents.NoSync())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prepared, err := store.Prepare(t.Context(), "after-upgrade", a.ID)
	if err != nil || prepared.Revision != 1 || prepared.Layers.UserVoice != "fixture voice" {
		t.Fatalf("agent changed during upgrade: %+v %v", prepared, err)
	}
	catalog, err := skills.Open(root, db, builtin.Builtins())
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	versions, err := catalog.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range versions {
		if v.ID == "persona-change" {
			found = true
			if _, err := catalog.Read(t.Context(), v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found {
		t.Fatal("upgraded installation lacks persona-change skill")
	}
}
