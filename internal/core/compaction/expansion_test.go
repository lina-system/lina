// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestExpansionReturnsExactScopedMaskedEvents(t *testing.T) {
	db, _ := passDB(t)
	events := ledger.New(db, nil)
	for _, id := range []string{"fact", "foreign"} {
		agent := "agent"
		if id == "foreign" {
			agent = "other"
		}
		_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: id, AgentID: agent, PersonID: "owner", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Body: "예약 번호는 42입니다. api_key=synthetic-hidden-value"})
		if err != nil {
			t.Fatal(err)
		}
	}
	expand := Expansion{DB: db, Events: events, NodeEvents: func(_ context.Context, agent, id string, offset, limit int) ([]string, int, error) {
		if agent != "agent" || id != "root" {
			return nil, 0, ErrExpand
		}
		return []string{"fact"}, 0, nil
	}}
	raw, err := expand.Read(t.Context(), "agent", "node:root", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var page ExpansionPage
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	original, err := events.Get(t.Context(), "fact")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != "fact" || page.Events[0].Body != original.Body.Text() || strings.Contains(string(raw), "synthetic-hidden-value") {
		t.Fatalf("inexact/unmasked expansion: %s", raw)
	}
	for _, id := range []string{"event:foreign", "event:missing", "other:fact"} {
		if body, err := expand.Read(t.Context(), "agent", id, 0, 50); err == nil || len(body) != 0 {
			t.Fatalf("expanded unavailable %s: %s %v", id, body, err)
		}
	}
	if _, err = expand.Read(t.Context(), "agent", "event:fact", -1, 50); !errors.Is(err, ErrExpand) {
		t.Fatal(err)
	}
}

func TestExpansionRefusesForgottenSourcesAndInvalidatedNodes(t *testing.T) {
	db, s, removals, _ := fixture(t)
	publish(t, s, proposal(t, s, "leaf"))
	expansion := Expansion{DB: db, Events: ledger.New(db, nil), NodeEvents: s.ExpandEvents}
	forget(t, db, removals)
	for _, id := range []string{"event:e1", "node:leaf"} {
		if raw, err := expansion.Read(t.Context(), "agent", id, 0, 50); err == nil || len(raw) != 0 {
			t.Fatalf("forgotten source returned %s", raw)
		}
	}
}
