// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestRememberToolRefusesCommitFailure(t *testing.T) {
	db := memoryDB(t)
	flow := newMemory(db, source, nil, nil)
	// The insert callback succeeds; the deferred FK fails SQLite's COMMIT.
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TABLE commit_probe(id TEXT REFERENCES memory_item(id) DEFERRABLE INITIALLY DEFERRED);
   CREATE TRIGGER fail_memory_commit AFTER INSERT ON memory_item BEGIN INSERT INTO commit_probe VALUES('missing'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	spec, handler := flow.RememberTool(memory.Write{Agent: "A", Person: "person"})
	raw, _ := json.Marshal(rememberInput("likes tea").Item)
	in, err := spec.Schema(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Write || in.Network || spec.Outward || spec.Placement != tools.Core {
		t.Fatal("memory tool placement/effect classification")
	}
	l := lease.New(func() {})
	defer l.Release()
	result, err := handler(t.Context(), l, in)
	if !errors.Is(err, sqlite3.CONSTRAINT_FOREIGNKEY) || result.State == tools.Applied || len(result.Body) != 0 {
		t.Fatalf("commit failure claimed remembered: %+v %v", result, err)
	}
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 0 {
		t.Fatalf("commit failure retained item: %+v %v", hits, err)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DROP TRIGGER fail_memory_commit")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result, err = handler(t.Context(), l, in)
	if err != nil || result.State != tools.Applied || result.Revision == "" {
		t.Fatalf("committed tool: %+v %v", result, err)
	}
	var body struct {
		Remembered bool
		ID         string
	}
	if err = json.Unmarshal(result.Body, &body); err != nil || !body.Remembered || body.ID == "" {
		t.Fatalf("receipt: %s %v", result.Body, err)
	}
}
func TestRecallToolBindsScopeAndRejectsUnknownArguments(t *testing.T) {
	db := memoryDB(t)
	flow := newMemory(db, source, nil, nil)
	if _, err := flow.Remember(t.Context(), rememberInput("likes tea")); err != nil {
		t.Fatal(err)
	}
	spec, handler := flow.RecallPersonTool(memory.RecallScope{Agent: "B", Person: "person"}, func() int64 { return 100 })
	if _, err := spec.Schema(json.RawMessage(`{"question":"What?","agent":"A"}`)); err == nil {
		t.Fatal("model could override agent scope")
	}
	in, err := spec.Schema(json.RawMessage(`{"question":"What?","query":"tea"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Write || in.Network {
		t.Fatal("recall classified as effect")
	}
	l := lease.New(func() {})
	defer l.Release()
	result, err := handler(context.Background(), l, in)
	if err != nil || result.State != tools.Applied {
		t.Fatalf("recall: %+v %v", result, err)
	}
	var answer PersonAnswer
	if err = json.Unmarshal(result.Body, &answer); err != nil {
		t.Fatal(err)
	}
	if len(answer.Sources) != 1 || answer.Sources[0].Evidence[0].Quote != "" {
		t.Fatalf("foreign source quote: %+v", answer)
	}
}
