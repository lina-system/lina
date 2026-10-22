// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func TestGrowthPayloadBoundsAcrossIngressAndWorker(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, err := store.Create(t.Context(), agents.Create{Name: "fixture", PersonID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	def, err := GrowthDefinition(store, a.ID, "person")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Store: db, Epoch: 1, Definitions: map[string]Definition{"growth": def}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Register(t.Context(), jobapi.Job{ID: "growth", Kind: jobapi.Memory, Enabled: true, Deterministic: true, Every: time.Nanosecond, Timeout: time.Second, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "run", "jobs.sock")
	listener, err := Listen(socket, controls)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Serve(ctx, listener) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := jobapi.NewClient(socket)
	for i, unit := range []string{"a", "가", "<"} {
		raw, err := json.Marshal(agents.GrowthCandidate{ID: unit, Expected: int64(i + 1), Snapshot: strings.Repeat(unit, 262144), Notice: "fixture learned"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err = service.SubmitDeterministic(ctx, "growth", raw); err != nil {
				t.Fatalf("large direct candidate: %v", err)
			}
		} else {
			if err = service.Enqueue(ctx, "growth", jobapi.Task{Sequence: int64(i + 1), Input: raw}); err != nil {
				t.Fatalf("large queued candidate: %v", err)
			}
			if _, err = client.Apply(ctx, jobapi.Proposal{Epoch: 1, JobID: "growth", From: int64(i), Through: int64(i + 1), Output: raw}); err != nil {
				t.Fatalf("large worker candidate: %v", err)
			}
		}
	}
	raw, _ := json.Marshal(agents.GrowthCandidate{ID: "over-snapshot-limit", Expected: 4, Snapshot: strings.Repeat("a", 262145), Notice: "fixture learned"})
	if err := service.SubmitDeterministic(ctx, "growth", raw); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("snapshot limit: %v", err)
	}
	huge, _ := json.Marshal(agents.GrowthCandidate{ID: "oversized", Expected: 4, Snapshot: "small", Notice: strings.Repeat("a", 6<<20)})
	if err := service.SubmitDeterministic(ctx, "growth", huge); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("whole candidate limit: %v", err)
	}
	if err := service.Enqueue(ctx, "growth", jobapi.Task{Sequence: 4, Input: huge}); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("queue limit: %v", err)
	}
	if _, err := client.Apply(ctx, jobapi.Proposal{Epoch: 1, JobID: "growth", From: 3, Through: 4, Output: huge}); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("worker limit: %v", err)
	}
	if got, err := store.Get(ctx, a.ID); err != nil || got.Revision != 4 {
		t.Fatalf("rejected candidate changed revision: %+v %v", got, err)
	}
	// Ordinary definitions still have the original byte cap.
	ordinary, _, _ := fixture(t)
	if err := ordinary.Enqueue(ctx, "memory", jobapi.Task{Sequence: 2, Input: json.RawMessage(`"` + strings.Repeat("a", 256<<10) + `"`)}); !errors.Is(err, jobapi.ErrInvalid) {
		t.Fatalf("ordinary job cap: %v", err)
	}
}
