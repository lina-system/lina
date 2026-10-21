// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type expansionMaterials struct {
	store  *assets.Store
	writer *assets.MaterialWriter
}
type expansionMaterialWrite struct{ *assets.PreparedMaterial }

func (p expansionMaterialWrite) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	a, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}
func (m expansionMaterials) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	p, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "ledger-body", assets.Actor{PersonID: "owner", AgentID: "agent"})
	if err != nil {
		return nil, err
	}
	return expansionMaterialWrite{p}, nil
}
func (m expansionMaterials) Read(ctx context.Context, ref ledger.Material) (string, error) {
	r, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	return string(data), err
}
func TestExpansionReadsFullMaterialAndReportsMissingBytes(t *testing.T) {
	db, cfg := passDB(t)
	root := filepath.Dir(cfg.Path)
	store, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	writer, err := assets.OpenMaterialWriter(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	events := ledger.New(db, expansionMaterials{store, writer})
	text := strings.Repeat("exact ", 4000) + "예약 42"
	event, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "large", AgentID: "agent", PersonID: "owner", ConversationID: "conversation", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Body: text})
	if err != nil {
		t.Fatal(err)
	}
	expansion := Expansion{DB: db, Events: events}
	raw, err := expansion.Read(t.Context(), "agent", "event:large", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	var page ExpansionPage
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Body != text || page.Events[0].Material.ID == "" {
		t.Fatal("material truncated")
	}
	asset, err := store.Get(t.Context(), event.Material.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, asset.Path)); err != nil {
		t.Fatal(err)
	}
	raw, err = expansion.Read(t.Context(), "agent", "event:large", 0, 1)
	if !errors.Is(err, ledger.ErrBytesUnavailable) || len(raw) != 0 {
		t.Fatalf("missing body claimed complete: %s %v", raw, err)
	}
}
