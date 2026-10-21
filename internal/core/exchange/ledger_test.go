// SPDX-License-Identifier: Apache-2.0
package exchange_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestDeliveryCommitsSharedConversationLedger(t *testing.T) {
	db := database(t)
	s := exchange.New(db)
	r := start(t, s)
	if _, err := s.Deliver(t.Context(), r.DeliveryID, now); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT id FROM conv_event WHERE conversation_id=?", r.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("shared ledger events=%d, want request and delivery", len(ids))
	}
	l := ledger.New(db, nil)
	for _, id := range ids {
		e, err := l.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if e.PersonID != "" || e.SpeakerKind != guard.Agent || e.Surface.Kind != "agent_exchange" || e.Origin.Entry != guard.EntryAgentExchange {
			t.Fatalf("wrong native provenance: %+v", e)
		}
		if e.ParentID == "" && !strings.Contains(e.Body.Text(), "wanted") {
			t.Fatalf("non-request event has no parent: %+v", e)
		}
	}
}

// The assets store writes through the same canon writer. This adapter proves
// material staging happens before the exchange transaction, without recursion.
type materialStore struct{ store *assets.Store }

func (m materialStore) Save(ctx context.Context, body secrets.Masked) (ledger.Material, error) {
	token, err := m.store.StageImport(ctx, strings.NewReader(body.Text()))
	if err != nil {
		return ledger.Material{}, err
	}
	a, err := m.store.Handover(ctx, token, "exchange-body", assets.Lina, assets.Actor{PersonID: "owner", AgentID: "answerer"})
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}
func (m materialStore) Read(ctx context.Context, ref ledger.Material) (string, error) {
	r, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	return string(data), err
}
func TestLargeExchangeUsesSharedMaterialsAndMasksSecrets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	db := openAt(t, root, 1)
	a, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := exchange.NewWithMaterials(db, materialStore{a})
	req := request()
	token := "ghp_" + strings.Repeat("Synthetic", 5)
	req.HandOff.Wanted = strings.Repeat("detail ", 4000) + token
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := s.Start(ctx, req, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Deliver(ctx, r.DeliveryID, now); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, r.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v %v", events, err)
	}
	if strings.Contains(events[0].Message.Body(), token) || strings.Contains(events[0].Message.Body(), secret.Placeholder) {
		t.Fatal("secret reached model evidence")
	}
	var materialID string
	if err = db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT material_id FROM conv_event WHERE conversation_id=? AND body IS NULL", r.ID).Scan(&materialID)
	}); err != nil || materialID == "" {
		t.Fatalf("material: %q %v", materialID, err)
	}
	asset, err := a.Get(ctx, materialID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, asset.Path)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Events(ctx, r.ID); !errors.Is(err, ledger.ErrBytesUnavailable) {
		t.Fatalf("missing bytes silently accepted: %v", err)
	}
}
