// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type observedReader struct {
	db     *canon.Store
	active bool
}

func (r *observedReader) Read(ctx context.Context, fn func(*canon.Tx) error) error {
	return r.db.Read(ctx, func(tx *canon.Tx) error { r.active = true; defer func() { r.active = false }(); return fn(tx) })
}

type materialBytes struct {
	store     *assets.Store
	writer    *assets.MaterialWriter
	reader    *observedReader
	nested    int
	afterRead func(ledger.Material)
}
type preparedBytes struct{ *assets.PreparedMaterial }

func (p preparedBytes) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	a, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}
func (m *materialBytes) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	p, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "test body", assets.Actor{PersonID: "person", AgentID: "agent"})
	return preparedBytes{p}, err
}
func (m *materialBytes) Read(ctx context.Context, ref ledger.Material) (string, error) {
	if m.reader.active {
		m.nested++
		return "", errors.New("material read holds canon snapshot")
	}
	file, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if m.afterRead != nil {
		fn := m.afterRead
		m.afterRead = nil
		fn(ref)
	}
	return string(data), err
}
func TestMaterialReadReleasesSnapshotAndRechecksRetraction(t *testing.T) {
	for _, mode := range []string{"large event", "revoked during read", "material trashed during read"} {
		revoke := mode != "large event"
		t.Run(mode, func(t *testing.T) {
			db, source, root := fixture(t)
			store, err := assets.Open(t.Context(), root, db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			writer, err := assets.OpenMaterialWriter(t.Context(), store)
			if err != nil {
				t.Fatal(err)
			}
			reader := &observedReader{db: db}
			materials := &materialBytes{store: store, writer: writer, reader: reader}
			id := seed(t, db, "input", guard.EntryOwner, materials)
			source.DB = reader
			source.Materials = materials
			if revoke {
				materials.afterRead = func(ref ledger.Material) {
					err := db.Write(t.Context(), func(tx *canon.Tx) error {
						if mode == "material trashed during read" {
							_, err := tx.ExecContext(t.Context(), "UPDATE assets SET trashed_at=1 WHERE id=?", ref.ID)
							return err
						}
						_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_block(item_id,person,holder,event_id,conversation_id) VALUES('forgotten','person','agent','input','')")
						return err
					})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			ep, err := source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, id)
			if materials.nested != 0 {
				t.Fatal("material I/O was attempted while holding the sole reader")
			}
			if revoke {
				if err == nil {
					t.Fatal("revoked material escaped after the read")
				}
			} else if err != nil || len(ep.Input.Text) <= ledger.InlineBytes {
				t.Fatalf("large input missing: bytes=%d err=%v", len(ep.Input.Text), err)
			} else {
				event, err := source.ReadEvent(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, "input")
				if err != nil || event.Text != ep.Input.Text || materials.nested != 0 {
					t.Fatalf("live large read: %v", err)
				}
			}
		})
	}
}
