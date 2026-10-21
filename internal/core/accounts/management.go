// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/provider"
)

// List returns metadata belonging to the authenticated person, never credentials.
func (s *Store) List(ctx context.Context, person string) ([]Account, error) {
	result := []Account{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM accounts ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var account Account
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &account); err != nil {
				return err
			}
			if account.Person == person {
				result = append(result, account)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, ErrStorage
	}
	return result, nil
}

// SignOut serializes with this account's existing actor and waits for active
// turns before recording intent. It keeps turn exclusion through deletion;
// interrupted retirement fails closed. Registration and owner binding survive.
func (s *Store) SignOut(ctx context.Context, person string, id provider.AccountID) error {
	_, err := s.call(ctx, id, func(a Account) (provider.Credential, error) {
		if a.Person != person {
			return provider.Credential{}, ErrNotFound
		}
		waiting, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		defer cancel()
		var metadataErr error
		err := s.vault.DeleteTokens(waiting, key(a), func() error {
			a.Status = NeedsSignIn
			metadataErr = s.save(s.ctx, a)
			return metadataErr
		})
		if metadataErr != nil {
			return provider.Credential{}, metadataErr
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return provider.Credential{}, err
			}
			return provider.Credential{}, ErrUnavailable
		}
		return provider.Credential{}, nil
	})
	return err
}
