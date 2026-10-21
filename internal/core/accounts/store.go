// SPDX-License-Identifier: Apache-2.0

// Package accounts owns account metadata and one refresh actor per account.
// Core opens one Store for its fenced canon writer. No account operation routes
// to another account or exposes credentials through its metadata projection.
package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/secret"
)

type Status string

const (
	OK          Status = "ok"
	NeedsSignIn Status = "needs_sign_in"
	NotEligible Status = "not_eligible"
	UsageCapped Status = "usage_capped"
)

func (s Status) valid() bool {
	switch s {
	case OK, NeedsSignIn, NotEligible, UsageCapped:
		return true
	}
	return false
}

var (
	ErrInvalid  = errors.New("invalid_account")
	ErrNotFound = errors.New("account_not_found")
	ErrConflict = errors.New("account_conflict")
	ErrStorage  = errors.New("account_storage_failed")
	ErrClosed   = errors.New("account_store_closed")
)

type PlanRef struct{ Name, Source string }
type Account struct {
	ID                            provider.AccountID
	Provider                      provider.ProviderID
	Label, Person                 string
	Auth                          provider.AuthKind
	ClientID, HostID              string
	Scopes                        []string
	AccessExpires, RefreshExpires *time.Time
	Status                        Status
	LastSuccess                   time.Time
	Plan                          *PlanRef
}
type Rotated struct {
	Tokens                        secrets.Tokens
	AccessExpires, RefreshExpires *time.Time
	Scopes                        []string // nil preserves the previously granted scopes.
}

// Refresher is the trusted issuer exchange seam. It receives only this account's
// refresh secret, must honor ctx, and must never log or forward it. Its errors
// are discarded; every failed exchange requires sign-in. It performs no retry.
type Refresher func(context.Context, Account, secret.Secret) (Rotated, error)
type Store struct {
	db        *canon.Store
	vault     *secrets.Store
	refresh   Refresher
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	actors    map[provider.AccountID]*actor
	closed    bool
	wg        sync.WaitGroup
	closeOnce sync.Once
	now       func() time.Time
}

var owners sync.Map

func Open(ctx context.Context, db *canon.Store, vault *secrets.Store, refresh Refresher) (*Store, error) {
	if db == nil || vault == nil {
		return nil, ErrInvalid
	}
	if _, loaded := owners.LoadOrStore(db, struct{}{}); loaded {
		return nil, ErrConflict
	}
	lifetime, cancel := context.WithCancel(ctx)
	s := &Store{db: db, vault: vault, refresh: refresh, ctx: lifetime, cancel: cancel, actors: make(map[provider.AccountID]*actor), now: time.Now}
	var ids []provider.AccountID
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM accounts")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id provider.AccountID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		cancel()
		owners.Delete(db)
		return nil, ErrStorage
	}
	for _, id := range ids {
		s.start(id)
	}
	return s, nil
}
func (s *Store) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		owners.Delete(s.db)
	})
}
func key(a Account) secrets.Key {
	return secrets.Key{Person: a.Person, Service: "provider:" + string(a.Provider), Name: string(a.ID)}
}
func textValid(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > 512 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func valid(a Account) bool {
	if !textValid(string(a.ID)) || !textValid(a.Person) || !textValid(a.Label) {
		return false
	}
	switch a.Provider {
	case provider.ChatGPT, provider.Claude, provider.Grok, provider.Gemini:
	default:
		return false
	}
	switch a.Auth {
	case provider.LoopbackOAuth:
		if !textValid(a.ClientID) || !textValid(a.HostID) {
			return false
		}
	case provider.APIKey, provider.LongLivedToken, provider.ObservedCLILogin:
	default:
		return false
	}
	return true
}
func get(ctx context.Context, tx *canon.Tx, id provider.AccountID) (Account, error) {
	var raw []byte
	var a Account
	err := tx.QueryRowContext(ctx, "SELECT record FROM accounts WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, ErrStorage
	}
	if json.Unmarshal(raw, &a) != nil || a.ID != id || !valid(a) || !a.Status.valid() {
		return Account{}, ErrStorage
	}
	return a, nil
}
func put(ctx context.Context, tx *canon.Tx, a Account) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO accounts(id,record) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record", a.ID, raw)
	return err
}
func (s *Store) Get(ctx context.Context, id provider.AccountID) (Account, error) {
	var a Account
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = get(ctx, tx, id); return err })
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Account{}, ErrNotFound
		}
		return Account{}, ErrStorage
	}
	return a, nil
}
func (s *Store) save(ctx context.Context, a Account) error {
	if err := s.db.Write(ctx, func(tx *canon.Tx) error { return put(ctx, tx, a) }); err != nil {
		return ErrStorage
	}
	return nil
}
func (s *Store) Add(ctx context.Context, a Account, r Rotated) error {
	if !valid(a) || !validRotation(a, r, s.now()) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return ErrClosed
	}
	if _, exists := s.actors[a.ID]; exists {
		return ErrConflict
	}
	// Persist an unusable record first; a crash at any later step fails closed.
	a.Status = NeedsSignIn
	a.LastSuccess = time.Time{}
	a.AccessExpires = nil
	a.RefreshExpires = nil
	if err := s.save(ctx, a); err != nil {
		return err
	}
	err := s.replace(ctx, a, r)
	s.start(a.ID)
	return err
}
func validRotation(a Account, r Rotated, now time.Time) bool {
	if a.Auth == provider.ObservedCLILogin {
		return true
	}
	if !r.Tokens.Valid(a.Auth == provider.LoopbackOAuth) {
		return false
	}
	if a.Auth == provider.LoopbackOAuth && r.AccessExpires == nil {
		return false
	}
	return (r.AccessExpires == nil || r.AccessExpires.After(now)) && (r.RefreshExpires == nil || r.RefreshExpires.After(now))
}
func (s *Store) replace(ctx context.Context, a Account, r Rotated) error {
	if !validRotation(a, r, s.now()) {
		return ErrInvalid
	}
	a.Status = NeedsSignIn
	if err := s.save(ctx, a); err != nil {
		return err
	}
	if a.Auth != provider.ObservedCLILogin {
		if err := s.vault.PutTokens(key(a), r.Tokens); err != nil {
			return ErrStorage
		}
	}
	return s.finish(ctx, a, r, OK)
}
func (s *Store) finish(ctx context.Context, a Account, r Rotated, status Status) error {
	if r.Scopes != nil {
		a.Scopes = slices.Clone(r.Scopes)
	}
	a.AccessExpires = r.AccessExpires
	a.RefreshExpires = r.RefreshExpires
	a.LastSuccess = s.now()
	a.Status = status
	if a.Provider == provider.ChatGPT && a.Auth == provider.LoopbackOAuth && !slices.Contains(a.Scopes, "chatgpt.tokens.use.direct") {
		a.Status = NotEligible
	}
	return s.save(ctx, a)
}
func (s *Store) Replace(ctx context.Context, id provider.AccountID, r Rotated) error {
	_, err := s.call(ctx, id, func(a Account) (provider.Credential, error) { return provider.Credential{}, s.replace(ctx, a, r) })
	return err
}

// Refresh requests one serialized exchange for an explicitly selected account.
// It is used after an authenticated rejection and by owner acceptance checks.
func (s *Store) Refresh(ctx context.Context, id provider.AccountID) error {
	_, err := s.call(ctx, id, func(a Account) (provider.Credential, error) {
		if a.Status == NeedsSignIn {
			return provider.Credential{}, failure(a.Status)
		}
		if a.Auth != provider.LoopbackOAuth {
			return provider.Credential{}, ErrInvalid
		}
		return provider.Credential{}, s.rotate(a)
	})
	return err
}
func (s *Store) SetStatus(ctx context.Context, id provider.AccountID, status Status) error {
	if !status.valid() {
		return ErrInvalid
	}
	_, err := s.call(ctx, id, func(a Account) (provider.Credential, error) {
		if a.Status == NeedsSignIn && status != NeedsSignIn {
			return provider.Credential{}, &provider.Failure{Kind: provider.NeedsSignIn}
		}
		a.Status = status
		return provider.Credential{}, s.save(ctx, a)
	})
	return err
}
func failure(s Status) error {
	switch s {
	case OK:
		return nil
	case NeedsSignIn:
		return &provider.Failure{Kind: provider.NeedsSignIn}
	case NotEligible:
		return &provider.Failure{Kind: provider.NotEligible}
	case UsageCapped:
		return &provider.Failure{Kind: provider.UsageCapped}
	}
	return ErrStorage
}
func (s *Store) Credential(ctx context.Context, id provider.AccountID) (provider.Credential, error) {
	return s.call(ctx, id, func(a Account) (provider.Credential, error) {
		if err := failure(a.Status); err != nil {
			return provider.Credential{}, err
		}
		if a.Provider == provider.ChatGPT && a.Auth == provider.LoopbackOAuth && !slices.Contains(a.Scopes, "chatgpt.tokens.use.direct") {
			a.Status = NotEligible
			err := s.save(ctx, a)
			if err != nil {
				return provider.Credential{}, err
			}
			return provider.Credential{}, failure(a.Status)
		}
		if a.Auth == provider.ObservedCLILogin {
			return provider.NewCredential(a.ID, a.Auth, ""), nil
		}
		if dueAt(a).Before(s.now()) || dueAt(a).Equal(s.now()) {
			if err := s.rotate(a); err != nil {
				return provider.Credential{}, err
			}
			var err error
			a, err = s.Get(s.ctx, a.ID)
			if err != nil {
				return provider.Credential{}, err
			}
		}
		tokens, err := s.vault.GetTokens(key(a))
		if err != nil {
			a.Status = NeedsSignIn
			if err := s.save(s.ctx, a); err != nil {
				return provider.Credential{}, err
			}
			return provider.Credential{}, failure(a.Status)
		}
		return tokens.Credential(a.ID, a.Auth), nil
	})
}
