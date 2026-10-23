// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/internal/core/turn"
)

type failNamingCommit struct{ turn.Canon }

func (c failNamingCommit) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	return c.Canon.Write(ctx, func(tx *canon.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return errors.New("injected commit failure")
	})
}

func TestNamingBreachAdmissionAtomicAndIdempotent(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	names := []memory.Item{{Kind: memory.Name, Name: &memory.NameValue{Current: "Birch", RetiredAliases: []string{"Cedar"}}}}
	now := time.Now()
	batch := []ledger.Admission{{Entry: guard.EntryAgent, Input: ledger.Input{ID: "naming-reply", ConversationID: "main:" + agent, AgentID: agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Cedar and Cedar."}}}
	admission := namingCanon{Canon: failNamingCommit{r.db}, person: r.person, agent: agent, names: names, replies: batch}
	if _, err := ledger.New(admission, nil).AdmitBatch(t.Context(), batch); err == nil {
		t.Fatal("injected failure ignored")
	}
	count, err := reply.NamingBreaches(t.Context(), r.db, r.person, agent)
	if err != nil || count != 0 {
		t.Fatalf("failed reply counted: %d %v", count, err)
	}
	if _, err := ledger.New(r.db, nil).Get(t.Context(), "naming-reply"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("failed reply committed", err)
	}
	admission.Canon = r.db
	if _, err := ledger.New(admission, nil).AdmitBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		return reply.RecordNamingBreach(t.Context(), tx, r.person, agent, "naming-reply", "Cedar and Cedar.", names)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.New(admission, nil).AdmitBatch(t.Context(), batch); err == nil {
		t.Fatal("append-only ledger accepted duplicate reply")
	}
	count, err = reply.NamingBreaches(t.Context(), r.db, r.person, agent)
	if err != nil || count != 1 {
		t.Fatalf("reply counted more than once: %d %v", count, err)
	}
	count, err = reply.NamingBreaches(t.Context(), r.db, r.person, "another")
	if err != nil || count != 0 {
		t.Fatalf("wrong agent counted: %d %v", count, err)
	}
}
