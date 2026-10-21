// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/persona"
)

func setup(t *testing.T) (*agents.Store, *canon.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db, root
}
func create(t *testing.T, s *agents.Store, preset string) agents.Agent {
	t.Helper()
	a, err := s.Create(t.Context(), agents.Create{Name: "Synthetic", PersonID: "person", Preset: preset, Layers: agents.Layers{Core: "core", DefaultVoice: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestRevisionAppliesAtNextPreparationAndSurvivesReopen(t *testing.T) {
	s, db, root := setup(t)
	a := create(t, s, "")
	b := create(t, s, "")
	first, err := s.Prepare(t.Context(), "turn-1", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	layers := first.Layers
	layers.UserVoice = "concise"
	changed, err := s.Revise(t.Context(), a.ID, a.Revision, layers, "person")
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision != a.Revision+1 {
		t.Fatal(changed)
	}
	pinned, err := s.Prepare(t.Context(), "turn-1", a.ID)
	if err != nil || !reflect.DeepEqual(pinned, first) {
		t.Fatalf("turn changed: %+v %v", pinned, err)
	}
	next, err := s.Prepare(t.Context(), "turn-2", a.ID)
	if err != nil || next.Revision != changed.Revision || next.Layers != layers {
		t.Fatalf("next: %+v %v", next, err)
	}
	if _, err := s.Prepare(t.Context(), "turn-1", b.ID); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("turn rebound: %v", err)
	}
	if _, err := s.Revise(t.Context(), a.ID, a.Revision, first.Layers, "person"); !errors.Is(err, agents.ErrConflict) {
		t.Fatalf("stale write: %v", err)
	}
	other, err := s.Prepare(t.Context(), "other", b.ID)
	if err != nil || other.Layers != first.Layers {
		t.Fatalf("other changed: %+v %v", other, err)
	}
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.Prepare(t.Context(), "turn-1", a.ID)
	if err != nil || !reflect.DeepEqual(persisted, first) {
		t.Fatalf("reopen: %+v %v", persisted, err)
	}
}
func TestPresetUpdatesOnlyItsCoreAndDefaultVoice(t *testing.T) {
	s, db, _ := setup(t)
	p := agents.Preset{ID: "sample", Core: "one", DefaultVoice: "voice one", BlankSettings: []string{"color"}}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	a, b := create(t, s, p.ID), create(t, s, p.ID)
	independent := create(t, s, "")
	before, err := s.Prepare(t.Context(), "before", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	layers := before.Layers
	layers.UserVoice = "user"
	layers.GrowthSnapshot = "growth"
	a, err = s.Revise(t.Context(), a.ID, a.Revision, layers, "person")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetState(t.Context(), a.ID, agents.State{Emotion: "calm", Settings: map[string]string{"color": "blue"}}, "person"); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_standing(grant_id,person,record) VALUES('grant','person',?)", []byte("unchanged"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.Core = "two"
	p.DefaultVoice = "voice two"
	p.BlankSettings = []string{"new"}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Prepare(t.Context(), "after", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Layers != (agents.Layers{Core: p.Core, DefaultVoice: p.DefaultVoice, UserVoice: "user", GrowthSnapshot: "growth"}) {
		t.Fatal(after)
	}
	state, err := s.State(t.Context(), a.ID)
	if err != nil || state.Emotion != "calm" || state.Settings["color"] != "blue" || len(state.Settings) != 1 {
		t.Fatalf("state: %+v %v", state, err)
	}
	for _, id := range []string{b.ID, independent.ID} {
		got, err := s.Prepare(t.Context(), id, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == b.ID && got.Layers.Core != "two" || id == independent.ID && got.Layers.Core != "core" {
			t.Fatal(got)
		}
	}
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		var record []byte
		err := tx.QueryRowContext(t.Context(), "SELECT record FROM authority_standing WHERE grant_id='grant'").Scan(&record)
		if string(record) != "unchanged" {
			t.Fatal("grant changed")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePreset(t.Context(), p, "person"); err != nil {
		t.Fatal(err)
	}
	same, err := s.Get(t.Context(), a.ID)
	if err != nil || same.Revision != after.Revision {
		t.Fatalf("duplicate update: %+v %v", same, err)
	}
}
func TestTrashEndsBindingsAndExpiresAtThirtyDays(t *testing.T) {
	s, _, root := setup(t)
	a, b := create(t, s, ""), create(t, s, "")
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Bind(t.Context(), "channel", a.ID, "person"); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind(t.Context(), "other", b.ID, "person"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(t.Context(), a.ID, "person", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveBinding(t.Context(), "channel"); !errors.Is(err, agents.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), a.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Prepare(t.Context(), "deleted", a.ID); !errors.Is(err, agents.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Bind(t.Context(), "channel", a.ID, "person"); !errors.Is(err, agents.ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), now.Add(30*24*time.Hour-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(t.Context(), a.ID, "person", now.Add(29*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveBinding(t.Context(), "channel"); !errors.Is(err, agents.ErrNotFound) {
		t.Fatal("restore revived binding", err)
	}
	if err := s.Delete(t.Context(), a.ID, "person", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(t.Context(), a.ID, "person", now.Add(30*24*time.Hour)); !errors.Is(err, agents.ErrExpired) {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "versions", "agents", a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("persona bytes remain", err)
	}
	tomb, err := s.Tombstone(t.Context(), a.ID)
	if err != nil || !tomb.DeletedAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("tombstone: %+v %v", tomb, err)
	}
	if got, err := s.ResolveBinding(t.Context(), "other"); err != nil || got != b.ID {
		t.Fatalf("other binding: %s %v", got, err)
	}
}
func TestFirstRunIsIdempotentAndOnlyNeedsChatGPT(t *testing.T) {
	s, db, root := setup(t)
	first, err := s.FirstRun(t.Context(), "person", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.Name != "LINA" || first.Agent.Preset != "lina" || first.SignInProvider != "chatgpt" {
		t.Fatal(first)
	}
	again, err := s.FirstRun(t.Context(), "person", true)
	if err != nil || again.Agent.ID != first.Agent.ID || again.SignInProvider != "" {
		t.Fatalf("again: %+v %v", again, err)
	}
	s2, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	restarted, err := s2.FirstRun(t.Context(), "person", false)
	if err != nil || restarted.Agent.ID != first.Agent.ID {
		t.Fatalf("restart: %+v %v", restarted, err)
	}
	if err := s.Delete(t.Context(), first.Agent.ID, "person", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.FirstRun(t.Context(), "person", false); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("resurrected default: %v", err)
	}
}

func TestPreparationKeepsAgentIdentityAndOriginalOwner(t *testing.T) {
	s, _, _ := setup(t)
	a := create(t, s, "")
	first, err := s.Prepare(t.Context(), "identity-first", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	layers := first.Layers
	layers.UserVoice = "Revised by another authorized editor"
	if _, err := s.Revise(t.Context(), a.ID, a.Revision, layers, "editor"); err != nil {
		t.Fatal(err)
	}
	next, err := s.Prepare(t.Context(), "identity-next", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Name != a.Name || next.OwnerPersonID != "person" || next.Layers != layers {
		t.Fatalf("identity or owner replaced by revision author: %+v", next)
	}
}

func TestFirstRunUpgradesOnlyShippedPlaceholderLayers(t *testing.T) {
	for _, customCore := range []bool{false, true} {
		t.Run(fmt.Sprint(customCore), func(t *testing.T) {
			s, _, _ := setup(t)
			legacy := agents.Preset{ID: "lina", Core: "<!-- SPDX-License-Identifier: Apache-2.0 -->\nLINA preset core placeholder. The project owner supplies the persona material.\n", DefaultVoice: "<!-- SPDX-License-Identifier: Apache-2.0 -->\nLINA preset default voice placeholder. The project owner supplies the voice material.\n"}
			if customCore {
				legacy.Core = "Owner-supplied core"
			}
			if err := s.UpdatePreset(t.Context(), legacy, "person"); err != nil {
				t.Fatal(err)
			}
			a := create(t, s, "lina")
			before, err := s.Prepare(t.Context(), "before-upgrade", a.ID)
			if err != nil {
				t.Fatal(err)
			}
			layers := before.Layers
			layers.UserVoice, layers.GrowthSnapshot = "Personal voice", "Personal growth"
			if _, err := s.Revise(t.Context(), a.ID, a.Revision, layers, "person"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FirstRun(t.Context(), "person", true); err != nil {
				t.Fatal(err)
			}
			after, err := s.Prepare(t.Context(), "after-upgrade", a.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := layers
			if !customCore {
				want.Core = persona.LinaCore
			}
			want.DefaultVoice = persona.LinaDefaultVoice
			if after.Layers != want || after.Revision != a.Revision+2 {
				t.Fatalf("placeholder upgrade lost personal layers or did not publish: %+v", after)
			}
			pinned, err := s.Prepare(t.Context(), "before-upgrade", a.ID)
			if err != nil || !reflect.DeepEqual(pinned, before) {
				t.Fatal("upgrade changed an already prepared turn", err)
			}
			if _, err := s.FirstRun(t.Context(), "person", true); err != nil {
				t.Fatal(err)
			}
			unchanged, err := s.Get(t.Context(), a.ID)
			if err != nil || unchanged.Revision != after.Revision {
				t.Fatal("upgrade was not idempotent", err)
			}
		})
	}
}
