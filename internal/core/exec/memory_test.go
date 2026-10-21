// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/provider"
)

func memoryDB(t *testing.T) *canon.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err = c.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: c, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}
func rememberInput(text string) memory.Write {
	return memory.Write{Person: "person", Agent: "A", Item: memory.Item{Kind: memory.Fact, Content: text, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "event", Precision: memory.Exact}}}}
}
func source(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
	return memory.Origin{Person: "person", Agent: "A", Speaker: guard.Person, Quote: "source statement", Scope: memory.Local}, nil
}
func TestRememberReceiptRequiresCommit(t *testing.T) {
	db := memoryDB(t)
	flow := NewMemory(db, source, nil, nil)
	failed := errors.New("injected_commit_failure")
	flow.write = func(ctx context.Context, fn func(*canon.Tx) error) error {
		return db.Write(ctx, func(tx *canon.Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			hits, err := memory.New(db).Recall(ctx, memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
			if err != nil || len(hits) != 0 {
				t.Fatalf("uncommitted item visible: %+v %v", hits, err)
			}
			return failed
		})
	}
	receipt, err := flow.Remember(t.Context(), rememberInput("likes tea"))
	if !errors.Is(err, failed) || receipt.Remembered() || receipt.ID != "" {
		t.Fatalf("failed write acknowledged: %+v %v", receipt, err)
	}
	flow.write = db.Write
	receipt, err = flow.Remember(t.Context(), rememberInput("likes tea"))
	if err != nil || !receipt.Remembered() {
		t.Fatalf("committed write: %+v %v", receipt, err)
	}
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || hits[0].ID != receipt.ID {
		t.Fatalf("receipt without durable item: %+v %v", hits, err)
	}
}
func TestRecallPersonDepthAndCitations(t *testing.T) {
	db := memoryDB(t)
	scope := memory.RecallScope{Agent: "A", Person: "person"}
	calls := 0
	aux := func(ctx context.Context, r RecallRequest) ([]Sentence, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second || r.Kind != provider.Auxiliary || len(r.Sources) > 12 {
			t.Fatalf("uncapped auxiliary: %+v", r)
		}
		return []Sentence{{Text: "Supported.", Citations: []Citation{r.Sources[0].Citation, {ID: "foreign", Revision: 1}}}, {Text: "Unsupported.", Citations: []Citation{{ID: "foreign", Revision: 1}}}, {Text: "Uncited."}, {Text: "Wrong revision.", Citations: []Citation{{ID: r.Sources[0].ID, Revision: 99}}}}, nil
	}
	flow := NewMemory(db, source, nil, aux)
	for i := 0; i < 15; i++ {
		if _, err := flow.Remember(t.Context(), rememberInput(fmt.Sprintf("fact %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What is known?", At: 100})
	if err != nil || calls != 0 || len(result.Sources) != 12 || len(result.Answer) != 0 {
		t.Fatalf("depth zero: %+v %v, calls=%d", result, err, calls)
	}
	result, err = flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What is known?", Depth: 1, At: 100})
	if err != nil || calls != 1 || len(result.Answer) != 1 || len(result.Answer[0].Citations) != 1 {
		t.Fatalf("citation validation: %+v %v", result, err)
	}
	_, err = flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "Think deeply", Depth: 2, At: 100})
	if !errors.Is(err, ErrBackgroundRequired) || calls != 1 {
		t.Fatalf("deeper work ran in turn: %v", err)
	}
}
func TestRecallPersonDiscardsCancelledAuxiliaryOutput(t *testing.T) {
	db := memoryDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	flow := NewMemory(db, source, nil, func(_ context.Context, r RecallRequest) ([]Sentence, error) {
		cancel()
		return []Sentence{{Text: "late", Citations: []Citation{r.Sources[0].Citation}}}, nil
	})
	if _, err := flow.Remember(t.Context(), rememberInput("likes tea")); err != nil {
		t.Fatal(err)
	}
	result, err := flow.RecallPerson(ctx, memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "What?", Depth: 1, At: 100})
	if !errors.Is(err, context.Canceled) || len(result.Answer) != 0 {
		t.Fatalf("late output accepted: %+v %v", result, err)
	}
}
