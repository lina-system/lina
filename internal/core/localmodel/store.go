// SPDX-License-Identifier: Apache-2.0

package localmodel

import (
	"context"
	"database/sql"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Write appends the serving model. It stores no task text.
func Write(ctx context.Context, db *canon.Store, row Row) error {
	if db == nil || len(row.ID) != 32 || len(row.Digest) != 64 || row.Provider == "" || row.Model == "" || (row.Route != RouteLocal && row.Route != RouteMain) || row.Reason == "" || row.Kind == "" || row.Created == "" {
		return ErrMalformed
	}
	input, output := nullCount(row.Input), nullCount(row.Output)
	return db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO model_served(id,kind,provider,model,route,reason,digest,input_tokens,output_tokens,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			row.ID, row.Kind, row.Provider, row.Model, row.Route, row.Reason, row.Digest, input, output, row.Created)
		return err
	})
}

func nullCount(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// CanonLedger records rows in the canon model_served table.
type CanonLedger struct{ DB *canon.Store }

func (c CanonLedger) Record(ctx context.Context, row Row) error { return Write(ctx, c.DB, row) }

// ReadModel returns the provider and model stored for one served id.
func ReadModel(ctx context.Context, db *canon.Store, id string) (provider string, model string, err error) {
	err = db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT provider, model FROM model_served WHERE id=?`, id).Scan(&provider, &model)
	})
	if err == sql.ErrNoRows {
		return "", "", ErrMalformed
	}
	return provider, model, err
}
