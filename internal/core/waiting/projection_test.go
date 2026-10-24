// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
)

// noWriter fails a build that reaches for canon's writer.
type noWriter struct{ waiting.Canon }

func (noWriter) Write(context.Context, func(*canon.Tx) error) error {
	return errors.New("the projection took canon's writer")
}

func handled(t *testing.T, db *canon.Store, id string) int {
	t.Helper()
	var count int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT handled FROM fixture WHERE id=?", id).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

// A projection runs on every commit, so it must not queue behind canon's
// writer. It hides a finished candidate and leaves its cleanup to Build.
func TestBuildProjectionReadsWithoutWriterAndLeavesCleanup(t *testing.T) {
	db, _ := database(t)
	src := &source{ids: []string{"open", "finished"}}
	put(t, db, "open", item())
	finished := item()
	finished.Finished = true
	put(t, db, "finished", finished)

	projection := waiting.New(noWriter{db}, map[string]waiting.Source{"work": src})
	list, err := projection.BuildProjection(t.Context(), "person", waiting.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(list), []string{"open"}) {
		t.Fatalf("projection rows = %v", ids(list))
	}
	if got := handled(t, db, "finished"); got != 0 {
		t.Fatalf("projection cleaned a finished candidate: handled=%d", got)
	}

	decision := waiting.New(db, map[string]waiting.Source{"work": src})
	list, err = decision.Build(t.Context(), "person", waiting.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(list), []string{"open"}) {
		t.Fatalf("build rows = %v", ids(list))
	}
	if got := handled(t, db, "finished"); got != 1 {
		t.Fatalf("build left a finished candidate: handled=%d", got)
	}
}
