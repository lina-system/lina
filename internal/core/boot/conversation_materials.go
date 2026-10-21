// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// Boot owns the L3 stores and composes immutable material bytes with ledger
// references. The material writer reconciles interrupted preparations at open.
type conversationMaterials struct {
	store  *assets.Store
	writer *assets.MaterialWriter
	actor  assets.Actor
}
type conversationMaterial struct{ *assets.PreparedMaterial }

func (p conversationMaterial) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	asset, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: asset.ID, Revision: asset.Revision}, err
}
func (m *conversationMaterials) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	prepared, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "conversation-body", m.actor)
	if err != nil {
		return nil, err
	}
	return conversationMaterial{prepared}, nil
}
func (m *conversationMaterials) Read(ctx context.Context, ref ledger.Material) (string, error) {
	file, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	return string(data), err
}

var errAdmittedInput = errors.New("input already admitted")

// Ledger prepares large masked bodies before Write, and closes their recovery
// handles after Write returns. This adapter adds inbox admission to that same
// transaction without moving filesystem preparation or cleanup under its lock.
type conversationAdmission struct {
	db      *canon.Store
	input   inbox.Input
	receipt inbox.Receipt
}

func (a *conversationAdmission) Read(ctx context.Context, fn func(*canon.Tx) error) error {
	return a.db.Read(ctx, fn)
}
func (a *conversationAdmission) Write(ctx context.Context, fn func(*canon.Tx) error) error {
	return a.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		a.receipt, err = inbox.Insert(ctx, tx, a.input)
		if err != nil {
			return err
		}
		if !a.receipt.Inserted {
			return errAdmittedInput
		}
		return fn(tx)
	})
}
func (r *conversationRuntime) journal() *ledger.Store { return ledger.New(r.db, r.materials) }
