// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/skills"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestPublishedPersonaCommitInManifest(t *testing.T) {
	cfg, _ := fixture(t)
	store, err := agents.Open(cfg.Root, cfg.Canon)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.FirstRun(t.Context(), "person-test", true)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(t.Context(), "before", first.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	layers := prepared.Layers
	layers.UserVoice = "synthetic revision"
	if _, err = store.Revise(t.Context(), first.Agent.ID, first.Agent.Revision, layers, "person-test"); err != nil {
		t.Fatal(err)
	}
	published, err := store.Prepare(t.Context(), "after", first.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Commit == prepared.Commit {
		t.Fatal("fixture did not revise persona")
	}
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, repo := range m.Repositories {
		found = found || repo.Path == "versions/agents/"+first.Agent.ID && repo.Head == published.Commit
	}
	if len(m.Repositories) != 8 || !found {
		t.Fatalf("manifest head=%+v want published %s", m.Repositories, published.Commit)
	}
	if _, err = Verify(t.Context(), filepath.Join(cfg.Root, "backups", m.GenerationID)); err != nil {
		t.Fatal(err)
	}
}
func TestWorkspaceLinksCannotExposeExcludedState(t *testing.T) {
	for _, mode := range []string{"absolute", "relative", "chained"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _ := fixture(t)
			for _, dir := range []string{"workspace", "secrets"} {
				if err := os.MkdirAll(filepath.Join(cfg.Root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(cfg.Root, "secrets/canary"), []byte("synthetic excluded bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			target := "../secrets/canary"
			if mode == "absolute" {
				target = filepath.Join(cfg.Root, "secrets/canary")
			}
			if mode == "chained" {
				if err := os.Symlink(".", filepath.Join(cfg.Root, "workspace/alias")); err != nil {
					t.Fatal(err)
				}
				target = "alias/../secrets/canary"
			}
			if err := os.Symlink(target, filepath.Join(cfg.Root, "workspace/leak")); err != nil {
				t.Fatal(err)
			}
			if _, err := Create(t.Context(), cfg); err == nil {
				t.Fatal("excluded state remains reachable through backup link")
			}
			noGeneration(t, cfg.Root)
		})
	}
}

func TestPublishedSkillCommitAndMissingObject(t *testing.T) {
	cfg, _ := fixture(t)
	store, err := skills.Open(cfg.Root, cfg.Canon, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var published skills.Applied
	for i, body := range []string{"First synthetic procedure.", "Updated synthetic procedure."} {
		published, err = store.Patch(t.Context(), skills.Patch{ID: "example", Expected: int64(i), Files: skills.Bundle{"SKILL.md": {Text: "---\nname: example\ndescription: Synthetic backup fixture.\n---\n" + body}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	m, err := Create(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repositories) != 1 || m.Repositories[0].Path != "versions/skills" || m.Repositories[0].Head != published.Version.Commit {
		t.Fatalf("skill head: %+v", m.Repositories)
	}
	path := filepath.Join(cfg.Root, "backups", m.GenerationID)
	if _, err := Verify(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	commit := published.Version.Commit
	if err := os.Remove(filepath.Join(cfg.Root, "versions/skills/objects", commit[:2], commit[2:])); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(t.Context(), cfg); err == nil {
		t.Fatal("missing published commit accepted")
	}
	// An earlier generation remains complete and readable.
	if _, err := Verify(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}
