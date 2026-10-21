// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
)

func (s *Store) checkSignInOwner(ctx context.Context, binding OwnerBinding, login bool) error {
	existing, err := NewOwnerBindings(s.db).Get(ctx)
	if errors.Is(err, ErrOwnerUnbound) {
		return nil
	}
	if err != nil {
		return ErrStorage
	}
	if existing.Person != binding.Person || login && existing != binding {
		return ErrOwnerMismatch
	}
	return nil
}

func (s *Store) admitSignIn(ctx context.Context, person, hostID string, selected signInSelection, tokens authorizedTokens) (Account, error) {
	binding := OwnerBinding{Person: person, Issuer: tokens.identity.Issuer, Subject: tokens.identity.Subject}
	if err := s.checkSignInOwner(ctx, binding, selected.login); err != nil {
		return Account{}, err
	}
	if selected.accountID != "" {
		var saved Account
		_, err := s.call(ctx, provider.AccountID(selected.accountID), func(current Account) (provider.Credential, error) {
			registered, err := s.registration(ctx, current.ID)
			if err != nil {
				return provider.Credential{}, err
			}
			if current.Person != person || current.Provider != provider.ChatGPT || current.Auth != provider.LoopbackOAuth || current.HostID != hostID || current.ClientID != tokens.clientID || registered.issuer != tokens.identity.Issuer || registered.subject != tokens.identity.Subject {
				return provider.Credential{}, ErrSignInIdentity
			}
			saved = verifiedAccount(current.ID, person, hostID, tokens, s.now())
			saved.Label, saved.Plan = current.Label, current.Plan
			return provider.Credential{}, s.persistSignIn(ctx, saved, binding, selected.login, tokens)
		})
		if err != nil {
			return Account{}, err
		}
		return saved, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return Account{}, ErrClosed
	}
	saved := verifiedAccount(provider.AccountID(randomSignInID()), person, hostID, tokens, s.now())
	if err := s.persistSignIn(ctx, saved, binding, selected.login, tokens); err != nil {
		// This ID was created only for this failed attempt. A failed cleanup leaves
		// an unusable record; no credential can be released by an account actor.
		cleanup := context.WithoutCancel(ctx)
		_ = s.vault.Delete(key(saved))
		_ = s.vault.Delete(idTokenKey(saved))
		_ = s.db.Write(cleanup, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(cleanup, "DELETE FROM accounts WHERE id=?", saved.ID)
			return err
		})
		return Account{}, err
	}
	s.start(saved.ID)
	return saved, nil
}

func idTokenKey(a Account) secrets.Key {
	k := key(a)
	k.Name += ".id-token"
	return k
}

func (s *Store) persistSignIn(ctx context.Context, a Account, binding OwnerBinding, login bool, t authorizedTokens) error {
	rotation := Rotated{Tokens: secrets.NewTokens(t.tokens.Access, t.tokens.Refresh), AccessExpires: a.AccessExpires, RefreshExpires: a.RefreshExpires}
	if !valid(a) || !validRotation(a, rotation, s.now()) {
		return ErrSignInInvalid
	}
	if err := s.checkSignInOwner(ctx, binding, login); err != nil {
		return err
	}
	marker := a
	marker.Status = NeedsSignIn
	marker.AccessExpires = nil
	marker.RefreshExpires = nil
	if err := s.save(ctx, marker); err != nil {
		return err
	}
	if err := s.vault.PutTokens(key(a), rotation.Tokens); err != nil {
		return ErrStorage
	}
	// Retain the ID token privately, without placing id_token_hint in a URL
	// relayed to the client: a credential must never cross that boundary.
	if err := s.vault.Put(idTokenKey(a), []byte(t.tokens.ID)); err != nil {
		return ErrStorage
	}
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := admitOwner(ctx, tx, binding, login); err != nil {
			return err
		}
		var issuer, subject string
		err := tx.QueryRowContext(ctx, "SELECT issuer,subject FROM sign_in_identities WHERE account_id=?", a.ID).Scan(&issuer, &subject)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, "INSERT INTO sign_in_identities(account_id,issuer,subject) VALUES(?,?,?)", a.ID, binding.Issuer, binding.Subject)
		} else if err == nil && (issuer != binding.Issuer || subject != binding.Subject) {
			return ErrSignInIdentity
		}
		if err != nil {
			return err
		}
		return put(ctx, tx, a)
	})
	if errors.Is(err, ErrOwnerMismatch) || errors.Is(err, ErrSignInIdentity) {
		return err
	}
	if err != nil {
		return ErrStorage
	}
	return nil
}
