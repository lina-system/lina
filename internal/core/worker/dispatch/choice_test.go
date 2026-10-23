// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"sync"
	"testing"
)

func TestDispatchAdmitsDisjointAreas(t *testing.T) {
	db := store(t)
	s, err := openService(t, Config{DB: db, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	d := definition(t)
	tool := s.Tool(Origin{Person: "person", Agent: "agent", Conversation: "main:agent", Turn: "turn:input"})
	for i, area := range []string{"src/**", "docs/**"} {
		d.WriteArea = []string{area}
		raw, _ := json.Marshal(d)
		result := tool.Execute(t.Context(), provider.ToolCall{ID: []string{"first", "second"}[i], Name: "dispatch_work", Arguments: string(raw)})
		if result.Code != loop.OK {
			t.Fatalf("disjoint task refused: %s", result.Code)
		}
	}
}

func TestAreaClaimsSerializeOverlapAndKeepFinishedLease(t *testing.T) {
	db := store(t)
	s, err := openService(t, Config{DB: db, Epoch: 1, Capacity: func(context.Context) int { return 8 }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	d := definition(t)
	records := []Record{
		{ID: "a", Definition: d, State: "queued"},
		{ID: "b", Definition: d, State: "queued"},
		{ID: "c", Definition: d, State: "queued"},
	}
	records[0].Definition.WriteArea = []string{"src/**"}
	records[1].Definition.WriteArea = []string{"src/auth/*.go"}
	records[2].Definition.WriteArea = []string{"docs/**"}
	for _, r := range records {
		if err := db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", r.ID, "turn", d.Repository, r.State, mustJSON(r))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := map[string]bool{}
	for i := range records {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := s.claim(t.Context(), &records[i])
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			claimed[records[i].ID] = ok
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if claimed["a"] == claimed["b"] || !claimed["c"] {
		t.Fatalf("overlap/disjoint claims: %v", claimed)
	}
	winner, loser := 0, 1
	if claimed["b"] {
		winner, loser = 1, 0
	}
	records[winner].State = "finished"
	if err = s.save(t.Context(), records[winner]); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.claim(t.Context(), &records[loser]); err != nil || ok || records[loser].Wait != "area" {
		t.Fatalf("terminal worker freed lease: %v %v %+v", ok, err, records[loser])
	}
	// A restart retains the exact same reservation, independent of in-memory maps.
	restarted := &Service{cfg: s.cfg}
	if ok, err := restarted.claim(t.Context(), &records[loser]); err != nil || ok {
		t.Fatalf("restart lost lease: %v %v", ok, err)
	}
	// Trusted application projection frees it; a worker's finished claim did not.
	card := work.Card{Applied: true}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_cards VALUES(?,?)", records[winner].ID, mustJSON(card))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.claim(t.Context(), &records[loser]); err != nil || !ok {
		t.Fatalf("landed task did not release: %v %v", ok, err)
	}
}

func TestWriteAreaValidationMatchesLeaseGlobs(t *testing.T) {
	if !allowedFiles("src/auth/main.go", []string{"src/**"}) {
		t.Fatal("recursive area accepted for lease but rejected for delivery")
	}
	if !allowedFiles("src/auth/main.go", []string{"src"}) {
		t.Fatal("directory area rejected for delivery")
	}
	if allowedFiles("private/secret.go", []string{"src/**"}) {
		t.Fatal("out-of-area file accepted")
	}
	if allowedFiles("private/[", []string{"src/**"}) {
		t.Fatal("literal filename became a glob")
	}
}
