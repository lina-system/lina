// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrOwnerMismatch = errors.New("owner_identity_mismatch")
	ErrOwnerUnbound  = errors.New("owner_identity_unbound")
	ErrSignInInvalid = errors.New("invalid_sign_in")
)

// OwnerBindingSchema belongs in a numbered canon migration. Provider accounts
// have independent lifetimes; removing one must never remove this binding.
const OwnerBindingSchema = `CREATE TABLE owner_binding (
	singleton INTEGER PRIMARY KEY CHECK (singleton=1),
	person TEXT NOT NULL CHECK (length(person)>0),
	issuer TEXT NOT NULL CHECK (length(issuer)>0),
	subject TEXT NOT NULL CHECK (length(subject)>0)
);`

type OwnerBinding struct{ Person, Issuer, Subject string }

type OwnerBindings struct{ db *canon.Store }

func NewOwnerBindings(db *canon.Store) *OwnerBindings { return &OwnerBindings{db: db} }

func (s *OwnerBindings) Get(ctx context.Context) (OwnerBinding, error) {
	var binding OwnerBinding
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT person,issuer,subject FROM owner_binding WHERE singleton=1").Scan(&binding.Person, &binding.Issuer, &binding.Subject)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return OwnerBinding{}, ErrOwnerUnbound
	}
	return binding, err
}

func (s *OwnerBindings) Bind(ctx context.Context, binding OwnerBinding) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error { return bindOwner(ctx, tx, binding) })
}

// bindOwner is called in the same writer transaction as the successful login's
// account metadata. The first writer wins; every later login must match it.
func bindOwner(ctx context.Context, tx *canon.Tx, binding OwnerBinding) error {
	if strings.TrimSpace(binding.Person) == "" || strings.TrimSpace(binding.Issuer) == "" || strings.TrimSpace(binding.Subject) == "" {
		return ErrSignInInvalid
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO owner_binding(singleton,person,issuer,subject) VALUES(1,?,?,?) ON CONFLICT(singleton) DO NOTHING", binding.Person, binding.Issuer, binding.Subject); err != nil {
		return err
	}
	var current OwnerBinding
	if err := tx.QueryRowContext(ctx, "SELECT person,issuer,subject FROM owner_binding WHERE singleton=1").Scan(&current.Person, &current.Issuer, &current.Subject); err != nil {
		return err
	}
	if current != binding {
		return ErrOwnerMismatch
	}
	return nil
}
