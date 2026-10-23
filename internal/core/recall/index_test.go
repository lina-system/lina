// SPDX-License-Identifier: Apache-2.0
package recall_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func database(t *testing.T) *canon.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return db
}
func admit(t *testing.T, db *canon.Store, id, body string, spans ...secrets.Span) {
	t.Helper()
	_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "conversation", PersonID: "owner", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(1700000000, 0), RecordedAt: time.Unix(1700000000, 0), TimeQuality: "observed", Body: body, SecretSpans: spans})
	if err != nil {
		t.Fatal(err)
	}
}
func count(t *testing.T, db *canon.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error { return tx.QueryRowContext(t.Context(), query, args...).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestAdmissionWritesContentlessIndexesAtomically(t *testing.T) {
	db := database(t)
	admit(t, db, "event", "도서관 공부 비밀", secrets.Span{Start: len("도서관 공부 "), End: len("도서관 공부 비밀")})
	for _, check := range []struct {
		query string
		want  int
	}{
		{"SELECT count(*) FROM recall_doc WHERE kind='event' AND source_id='event' AND person='owner' AND holder='agent'", 1},
		{"SELECT count(*) FROM recall_text WHERE recall_text MATCH '도서관'", 1},
		{"SELECT count(*) FROM recall_pair WHERE recall_pair MATCH '공부'", 1},
		{"SELECT count(*) FROM recall_pair WHERE recall_pair MATCH '비밀'", 0},
		{"SELECT count(*) FROM recall_text WHERE body IS NOT NULL", 0},
		{"SELECT count(*) FROM recall_pair WHERE body IS NOT NULL", 0},
		{"SELECT count(*) FROM recall_pending", 0},
		{"SELECT count(*) FROM ledger_search_text", 1},
	} {
		if got := count(t, db, check.query); got != check.want {
			t.Fatalf("%s: %d, want %d", check.query, got, check.want)
		}
	}
}

func TestAdmissionKeepsTextFollowingMaskedAssignment(t *testing.T) {
	db := database(t)
	admit(t, db, "assignment", "password=hunter2 도서관")
	if n := count(t, db, "SELECT count(*) FROM ledger_search WHERE ledger_search MATCH '도서관'"); n != 1 {
		t.Fatal("fixture must be searchable in the ledger")
	}
	if n := count(t, db, "SELECT count(*) FROM recall_text WHERE recall_text MATCH '도서관'"); n != 1 {
		t.Fatal("derived text was masked twice")
	}
}
