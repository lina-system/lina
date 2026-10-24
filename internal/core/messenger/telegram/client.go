// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/thisisjun786/lina/internal/core/secrets"
)

const (
	// apiIDName and apiHashName are the person's own application credentials.
	apiIDName   = "api_id"
	apiHashName = "api_hash"
	// sessionPrefix names one account's signed-in session.
	sessionPrefix = "session:"
)

// vaultSession keeps the MTProto session in the secret store. The session holds
// the auth key, so it is never written as a plaintext file and never leaves
// Core except to the platform connection.
type vaultSession struct {
	store *secrets.Store
	key   secrets.Key
}

func (v vaultSession) LoadSession(context.Context) ([]byte, error) {
	blob, err := v.store.GetBlob(v.key)
	if errors.Is(err, secrets.ErrNotFound) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return blob.Bytes(), nil
}

func (v vaultSession) StoreSession(ctx context.Context, data []byte) error {
	for {
		err := v.store.PutBlob(v.key, data)
		if !errors.Is(err, secrets.ErrInTurn) {
			return err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func credentialKey(person, name string) secrets.Key {
	return secrets.Key{Person: person, Service: "telegram", Name: name}
}

// PutCredentials stores the person's own api_id and api_hash. The app or TUI
// calls it after the person creates the application at my.telegram.org; LINA
// never does that step.
func PutCredentials(store *secrets.Store, person string, apiID int, apiHash string) error {
	if store == nil || apiID <= 0 || apiHash == "" || strings.ContainsRune(apiHash, 0) {
		return ErrInvalid
	}
	if err := store.PutBlob(credentialKey(person, apiIDName), []byte(strconv.Itoa(apiID))); err != nil {
		return err
	}
	return store.PutBlob(credentialKey(person, apiHashName), []byte(apiHash))
}

// credentials returns the person's application credentials.
func (a *Adapter) credentials() (int, string, error) {
	rawID, err := a.cfg.Vault.GetBlob(credentialKey(a.cfg.Person, apiIDName))
	if err != nil || !rawID.Ready() {
		return 0, "", ErrNoCredentials
	}
	id, err := strconv.Atoi(string(rawID.Bytes()))
	if err != nil || id <= 0 {
		return 0, "", ErrNoCredentials
	}
	rawHash, err := a.cfg.Vault.GetBlob(credentialKey(a.cfg.Person, apiHashName))
	if err != nil || !rawHash.Ready() {
		return 0, "", ErrNoCredentials
	}
	hash := string(rawHash.Bytes())
	if hash == "" || strings.ContainsRune(hash, 0) {
		return 0, "", ErrNoCredentials
	}
	return id, hash, nil
}

// DeleteSession removes one account's session. Disconnecting a source uses it
// so the next connection signs in again.
func DeleteSession(store *secrets.Store, person, account string) error {
	if store == nil {
		return ErrInvalid
	}
	return store.Delete(credentialKey(person, sessionPrefix+account))
}

// Account returns the signed-in user's platform identity, never a client claim.
func (a *Adapter) Account(ctx context.Context) (string, error) {
	client, err := a.ensure(ctx)
	if err != nil {
		return "", err
	}
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return "", err
	}
	if !status.Authorized || status.User == nil || status.User.Bot {
		return "", ErrInvalid
	}
	return strconv.FormatInt(status.User.ID, 10), nil
}

// SaveSession copies the authenticated session under the verified account ID.
func (a *Adapter) SaveSession(ctx context.Context) (string, error) {
	account, err := a.Account(ctx)
	if err != nil {
		return "", err
	}
	if err := a.Close(); err != nil {
		return "", err
	}
	blob, err := a.cfg.Vault.GetBlob(credentialKey(a.cfg.Person, sessionPrefix+a.cfg.Account))
	if err != nil {
		return "", err
	}
	if err := a.cfg.Vault.PutBlob(credentialKey(a.cfg.Person, sessionPrefix+account), blob.Bytes()); err != nil {
		return "", err
	}
	return account, nil
}

func (a *Adapter) open() error {
	apiID, apiHash, err := a.credentials()
	if err != nil {
		return err
	}
	options := a.cfg.Options
	options.SessionStorage = vaultSession{
		store: a.cfg.Vault,
		key:   credentialKey(a.cfg.Person, sessionPrefix+a.cfg.Account),
	}
	if options.Device.DeviceModel == "" {
		options.Device.DeviceModel = "LINA"
		options.Device.SystemVersion = "Linux"
		options.Device.AppVersion = "1"
		options.Device.SystemLangCode = "en"
		options.Device.LangCode = "en"
	}
	dispatcher := tg.NewUpdateDispatcher()
	options.UpdateHandler = &dispatcher
	client := telegram.NewClient(apiID, apiHash, options)
	a.client, a.dispatcher = client, &dispatcher
	a.handleUpdates()
	return nil
}

// start runs one client connection for the adapter's lifetime. Its readiness
// channel and its one-shot signal belong to this run, so a replaced connection
// can never release a later start's waiter.
func (a *Adapter) start(client *telegram.Client, ready chan struct{}, once *sync.Once) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.cancel, a.done = cancel, done
	markReady := func(err error) {
		once.Do(func() {
			a.mu.Lock()
			a.startErr = err
			a.mu.Unlock()
			close(ready)
		})
	}
	go func() {
		defer close(done)
		err := client.Run(ctx, func(ctx context.Context) error {
			markReady(nil)
			<-ctx.Done()
			return nil
		})
		if ctx.Err() != nil && (err == nil || errors.Is(err, context.Canceled)) {
			return
		}
		// A connection that never became ready still has to release the
		// waiter, so its cause reaches the source registry.
		a.mu.Lock()
		a.runErr = err
		a.client, a.dispatcher = nil, nil
		a.mu.Unlock()
		markReady(err)
	}()
}

// ensure starts the client once and waits until it can serve requests.
func (a *Adapter) ensure(ctx context.Context) (*telegram.Client, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, ErrClosed
	}
	if a.client == nil {
		if err := a.open(); err != nil {
			a.mu.Unlock()
			return nil, err
		}
		a.ready = make(chan struct{})
		a.readyOnce = new(sync.Once)
		a.startErr, a.runErr = nil, nil
		a.start(a.client, a.ready, a.readyOnce)
	}
	client, ready := a.client, a.ready
	a.mu.Unlock()
	select {
	case <-ready:
		a.mu.Lock()
		err := a.startErr
		if err == nil {
			err = a.runErr
		}
		client = a.client
		a.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if client == nil {
			return nil, ErrClosed
		}
		return client, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// api returns the connected API client.
func (a *Adapter) api(ctx context.Context) (*tg.Client, error) {
	client, err := a.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return client.API(), nil
}

// Close stops the connection. The session stays in the secret store.
func (a *Adapter) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}
