// SPDX-License-Identifier: Apache-2.0

// Package budget reserves and settles the shared token budget inside canon's
// fenced transaction. Callers install Schema through their canon migration.
package budget

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/provider"
)

var (
	ErrLimit   = errors.New("budget_reached")
	ErrInvalid = errors.New("invalid_budget_record")
	ErrSettled = errors.New("budget_already_settled")
)

const Schema = `CREATE TABLE budget_usage (
 id TEXT PRIMARY KEY, agent TEXT NOT NULL, account TEXT NOT NULL,
 provider TEXT NOT NULL, kind TEXT NOT NULL, run TEXT NOT NULL,
 reserved INTEGER NOT NULL CHECK(reserved >= 0),
 charged INTEGER NOT NULL CHECK(charged >= 0),
 settled INTEGER NOT NULL DEFAULT 0 CHECK(settled IN (0,1)),
 usage TEXT NOT NULL
)`

type Reservation struct {
	ID, Agent string
	Account   provider.AccountID
	Provider  provider.ProviderID
	Kind      provider.RunKind
	Run       string
	Tokens    int64
}
type Row struct {
	Reservation
	Charged int64
	Settled bool
	Usage   provider.Usage
}

// Reserve atomically counts every agent and run kind against one limit. Zero
// allowance admits no new call, including a zero-sized estimate.
func Reserve(ctx context.Context, tx *canon.Tx, r Reservation, limit int64) error {
	if r.ID == "" || r.Agent == "" || r.Account == "" || r.Provider == "" || !r.Kind.Valid() || r.Run == "" || r.Tokens < 0 || limit < 0 {
		return ErrInvalid
	}
	var used int64
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(charged),0) FROM budget_usage").Scan(&used); err != nil {
		return err
	}
	if used >= limit || r.Tokens > limit-used {
		return ErrLimit
	}
	unknown, _ := json.Marshal(provider.Usage{})
	_, err := tx.ExecContext(ctx, `INSERT INTO budget_usage(id,agent,account,provider,kind,run,reserved,charged,usage) VALUES(?,?,?,?,?,?,?,?,?)`, r.ID, r.Agent, r.Account, r.Provider, r.Kind, r.Run, r.Tokens, r.Tokens, string(unknown))
	return err
}

// Settle records only reported usage. Missing totals retain the reservation
// charge (or a larger reported subtotal), never turn missing usage into zero.
// Cached/reasoning counts are subsets and are not added to input/output again.
func Settle(ctx context.Context, tx *canon.Tx, id string, u provider.Usage) error {
	if !u.Valid() {
		return ErrInvalid
	}
	row, err := Read(ctx, tx, id)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(u)
	if err != nil {
		return err
	}
	if row.Settled {
		previous, _ := json.Marshal(row.Usage)
		if string(previous) == string(encoded) {
			return nil
		}
		return ErrSettled
	}
	var charged int64
	for _, n := range []*int64{u.Input, u.Output} {
		if n != nil {
			if *n > math.MaxInt64-charged {
				return ErrInvalid
			}
			charged += *n
		}
	}
	if u.Input == nil || u.Output == nil {
		charged = max(charged, row.Tokens)
	}
	_, err = tx.ExecContext(ctx, "UPDATE budget_usage SET charged=?,settled=1,usage=? WHERE id=?", charged, string(encoded), id)
	return err
}
func Read(ctx context.Context, tx *canon.Tx, id string) (Row, error) {
	var r Row
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT id,agent,account,provider,kind,run,reserved,charged,settled,usage FROM budget_usage WHERE id=?`, id).Scan(&r.ID, &r.Agent, &r.Account, &r.Provider, &r.Kind, &r.Run, &r.Tokens, &r.Charged, &r.Settled, &encoded)
	if err != nil {
		return Row{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &r.Usage); err != nil {
		return Row{}, err
	}
	return r, nil
}
