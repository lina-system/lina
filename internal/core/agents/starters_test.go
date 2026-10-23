// SPDX-License-Identifier: Apache-2.0

package agents_test

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestFirstRunPreservesLegacyIdentityAndUserRevisions(t *testing.T) {
	t.Parallel()
	s, db, _ := setup(t)
	if err := s.UpdatePreset(t.Context(), agents.Preset{ID: "lina", Core: "fixture core", DefaultVoice: "fixture voice"}, "person"); err != nil {
		t.Fatal(err)
	}
	legacy := create(t, s, "lina")
	custom := create(t, s, "")
	layers := agents.Layers{Core: "fixture core", DefaultVoice: "fixture voice", UserVoice: "personal voice", GrowthSnapshot: "personal growth"}
	legacy, err := s.Revise(t.Context(), legacy.ID, legacy.Revision, layers, "person")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := s.Prepare(t.Context(), "legacy-turn", legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The previous implementation recorded only this singleton, including its
	// original user-chosen name. It had no per-preset receipts.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO agent_first_run VALUES(1,?)", legacy.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		first, err := s.FirstRun(t.Context(), "person", true)
		if err != nil || first.Agent != legacy {
			t.Fatalf("existing initialized identity changed: %+v %v", first, err)
		}
		if got, err := s.Get(t.Context(), custom.ID); err != nil || got != custom {
			t.Fatalf("user-created agent changed: %+v %v", got, err)
		}
		if got, err := s.Prepare(t.Context(), "legacy-turn", legacy.ID); err != nil || got != pinned {
			t.Fatalf("existing prepared revision changed: %+v %v", got, err)
		}
	}
	if err := s.Delete(t.Context(), legacy.ID, "person", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FirstRun(t.Context(), "person", true); !errors.Is(err, agents.ErrNotFound) {
		t.Fatalf("deleted legacy default replaced: %v", err)
	}
}

var starterNames = map[string]string{
	"system": "Selene", "lina": "Lina", "dev": "Sion", "knowledge": "Rumi",
	"life": "Arin", "creative": "Cleo", "investing": "Kai", "legal": "Kanna",
}

func assertStarters(t *testing.T, s *agents.Store) map[string]agents.Agent {
	t.Helper()
	rows, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(starterNames) {
		t.Fatalf("starter count = %d; want %d", len(rows), len(starterNames))
	}
	byPreset := map[string]agents.Agent{}
	ids := map[string]bool{}
	for _, a := range rows {
		if starterNames[a.Preset] != a.Name || ids[a.ID] || byPreset[a.Preset].ID != "" {
			t.Fatalf("duplicate or incorrect starter: %+v", a)
		}
		ids[a.ID] = true
		byPreset[a.Preset] = a
	}
	return byPreset
}

func TestFirstRunCreatesEightStartersFromCurrentPresets(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	for key := range starterNames {
		if err := s.UpdatePreset(t.Context(), agents.Preset{ID: key, Core: "core-" + key, DefaultVoice: "voice-" + key}, "person"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.FirstRun(t.Context(), "person", false)
	if err != nil {
		t.Fatal(err)
	}
	rows := assertStarters(t, s)
	if first.Agent != rows["lina"] || first.SignInProvider != "chatgpt" {
		t.Fatalf("first conversation or prerequisite: %+v", first)
	}
	for key, a := range rows {
		p, err := s.Prepare(t.Context(), "first-"+key, a.ID)
		if err != nil || p.Layers.Core != "core-"+key || p.Layers.DefaultVoice != "voice-"+key {
			t.Fatalf("starter did not use current preset %s: %+v %v", key, p, err)
		}
	}
}

func TestFirstRunConcurrentStoresAndRestartKeepEightIdentities(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	other, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store := []*agents.Store{s, other}[i%2]
			if _, err := store.FirstRun(t.Context(), "person", true); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	before := assertStarters(t, s)
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.FirstRun(t.Context(), "person", true); err != nil {
		t.Fatal(err)
	}
	if after := assertStarters(t, reopened); !reflect.DeepEqual(before, after) {
		t.Fatalf("restart changed identities: before=%v after=%v", before, after)
	}
}

func TestFirstRunResumesAfterInterruptedPublication(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	exec := func(sql string) {
		t.Helper()
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), sql)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TRIGGER interrupt_starters BEFORE INSERT ON agents WHEN NEW.preset='life' BEGIN SELECT RAISE(ABORT,'injected interruption'); END`)
	if _, err := s.FirstRun(t.Context(), "person", true); err == nil {
		t.Fatal("initialization ignored the injected publication failure")
	}
	before, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	exec("DROP TRIGGER interrupt_starters")
	reopened, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.FirstRun(t.Context(), "person", true); err != nil {
		t.Fatal(err)
	}
	after := assertStarters(t, reopened)
	for _, a := range before {
		if after[a.Preset] != a {
			t.Fatalf("replaced committed starter on retry: %+v", a)
		}
	}
}

func TestFirstRunDoesNotReplaceRenamedDeletedOrExpiredStarters(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	if _, err := s.FirstRun(t.Context(), "person", true); err != nil {
		t.Fatal(err)
	}
	before := assertStarters(t, s)
	for key, a := range before {
		if key == "lina" {
			continue
		}
		if _, err := s.Rename(t.Context(), a.ID, "Renamed "+key, "person"); err != nil {
			t.Fatal(err)
		}
	}
	deleted := before["system"]
	now := time.Now()
	if err := s.Delete(t.Context(), deleted.ID, "person", now); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := s.FirstRun(t.Context(), "person", true); err != nil {
			t.Fatal(err)
		}
		rows, err := s.List(t.Context())
		if err != nil || len(rows) != 7 {
			t.Fatalf("recreated deleted starter: %v %v", rows, err)
		}
		for _, a := range rows {
			if a.ID != before[a.Preset].ID || a.Preset != "lina" && a.Name != "Renamed "+a.Preset {
				t.Fatalf("replaced renamed starter: %+v", a)
			}
		}
		if i == 0 {
			if err := s.Expire(t.Context(), now.Add(31*24*time.Hour)); err != nil {
				t.Fatal(fmt.Errorf("expire starter: %w", err))
			}
		}
	}
}
