// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
)

func TestSettingsPersonaAndBindingChangesDoNotMintEpoch(t *testing.T) {
	root := restartRoot(t)
	s, epoch := restartStore(t, root)
	x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: s, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	a, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	created, err := a.Create(t.Context(), agents.Create{Name: "Synthetic", PersonID: "person", Layers: agents.Layers{Core: "core", DefaultVoice: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func() error
	}{
		{"settings", func() error {
			if err := a.SetState(t.Context(), created.ID, agents.State{Settings: map[string]string{"style": "concise"}}, "person"); err != nil {
				return err
			}
			state, err := a.State(t.Context(), created.ID)
			if err == nil && state.Settings["style"] != "concise" {
				t.Fatal("settings did not apply")
			}
			return err
		}},
		{"persona", func() error {
			changed, err := a.Revise(t.Context(), created.ID, created.Revision, agents.Layers{Core: "core", DefaultVoice: "voice", UserVoice: "concise"}, "person")
			if err != nil {
				return err
			}
			prepared, err := a.Prepare(t.Context(), "next-turn", created.ID)
			if err == nil && (prepared.Revision != changed.Revision || prepared.Layers.UserVoice != "concise") {
				t.Fatal("persona did not apply")
			}
			return err
		}},
		{"binding", func() error {
			if err := a.Bind(t.Context(), "synthetic-channel", created.ID, "person"); err != nil {
				return err
			}
			id, err := a.ResolveBinding(t.Context(), "synthetic-channel")
			if err == nil && id != created.ID {
				t.Fatal("binding did not apply")
			}
			return err
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			if err := change.apply(); err != nil {
				t.Fatal(err)
			}
			if got, err := s.Epoch(); err != nil || got != epoch {
				t.Fatalf("hot reload minted epoch: %d %v", got, err)
			}
			if x.Status().Owed() {
				t.Fatal("hot reload requested restart")
			}
			if exit, _, err := x.Step(t.Context()); exit || err != nil {
				t.Fatalf("hot reload exited: %v %v", exit, err)
			}
		})
	}
}
