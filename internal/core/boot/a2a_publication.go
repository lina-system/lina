// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/thisisjun786/lina/kit/secret"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	a2a "github.com/thisisjun786/lina/internal/core/a2a/publication"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// A2AOptions contains only in-process acceptance seams. Owner configuration
// lives in system/core/a2a.json; no environment variable bypasses TLS/tailnet.
type A2AOptions struct {
	Listen    func(context.Context, string, string) (net.Listener, error)
	Transport http.RoundTripper
}
type publicationRuntime struct {
	recovered atomic.Bool
	root      *conversationRuntime
	store     *a2a.Store
	server    *a2a.Server
	stop      context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
}

func (r *conversationRuntime) initPublications(ctx context.Context) error {
	p := &publicationRuntime{root: r, store: &a2a.Store{DB: r.db}, wake: make(chan struct{}, 1)}
	p.server = &a2a.Server{Store: p.store, Config: p.config, Secret: func(oauth a2a.OAuth) (secret.Secret, error) {
		value, err := r.vault.Get(secrets.Key{Person: r.person, Service: a2a.CredentialService(oauth), Name: oauth.SecretName})
		if err != nil {
			return secret.Secret{}, err
		}
		return value, nil
	}, Wake: func() {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}}
	if _, err := p.config(); err != nil {
		return err
	}
	if err := p.store.Recover(ctx); err != nil {
		return err
	}
	r.publications = p
	return nil
}
func (p *publicationRuntime) config() (a2a.Config, error) {
	raw, err := p.root.controls.Read("a2a.json")
	if errors.Is(err, os.ErrNotExist) {
		return a2a.Config{}, nil
	}
	if err != nil {
		return a2a.Config{}, err
	}
	return a2a.ParseConfig(raw)
}
func (p *publicationRuntime) authorize(ctx context.Context, rec a2a.Record) error {
	if _, _, err := p.server.Authorized(rec, rec.Principal); err != nil {
		return err
	}
	if rec.CancelRequested || time.Now().Unix() >= rec.Deadline {
		return a2a.ErrDenied
	}
	_, err := p.root.agents.Get(ctx, rec.Agent)
	return err
}
func (r *conversationRuntime) validateA2A(ctx context.Context) error {
	if r.externalTask == "" {
		return nil
	}
	if r.publications == nil {
		return a2a.ErrDenied
	}
	rec, err := r.publications.store.Read(ctx, r.externalTask)
	if err != nil {
		return err
	}
	if err = r.publications.authorize(ctx, rec); err != nil {
		return err
	}
	return r.publications.approved(ctx, rec)
}
func startA2AListener(ctx context.Context, r *conversationRuntime, opts *A2AOptions, ready func() bool) (func(context.Context) error, <-chan error, error) {
	if r == nil || r.publications == nil {
		return nil, nil, nil
	}
	p := r.publications
	c, err := p.config()
	if err != nil || !c.Enabled {
		return nil, nil, err
	}
	cert, err := tls.LoadX509KeyPair(c.Certificate, c.PrivateKey)
	if err != nil {
		return nil, nil, errors.New("A2A TLS configuration unavailable")
	}
	cfg := clientsrv.TailnetConfig{Address: c.Address, Socket: c.TailscaleSocket}
	if opts != nil {
		cfg.Listen = opts.Listen
		p.server.Transport = opts.Transport
	}
	listener, err := clientsrv.ListenTailnet(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	p.server.Ready = func() bool { return ready() && p.recovered.Load() }
	server := &http.Server{Handler: p.server, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	finished := make(chan struct{})
	var servingError error
	go func() {
		defer close(finished)
		err := server.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		servingError = err
		done <- err
	}()
	return func(phase context.Context) error {
		err := server.Close()
		select {
		case <-finished:
			return errors.Join(err, servingError)
		case <-phase.Done():
			return errors.Join(err, phase.Err())
		}
	}, done, nil
}
