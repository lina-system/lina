// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/kit/protocol"
)

// MessengerConfig supplies the adapters Core reads messenger sources with. The
// source registry, admission and the sync loop are Core's; adapters are the
// platform boundary. A platform without an adapter is left untouched.
type MessengerConfig struct {
	// Adapters are keyed by platform.
	Adapters map[string]messenger.Adapter
	// Interval is the schedule for a source whose adapter states none.
	Interval time.Duration
	// WebexBaseURL and WebexTransport are in-process test seams for the Webex
	// adapter. Production leaves both empty.
	WebexBaseURL   string
	WebexTransport http.RoundTripper
	WebexInterval  time.Duration
}

// startMessenger opens the messenger store on this conversation's canon and
// reads connected sources on their schedule until Core stops. The Webex
// adapter is always present: it reads only the sources the person connected,
// and its sign-in runs through Core's client browser relay.
func (r *conversationRuntime) startMessenger(ctx context.Context, cfg *MessengerConfig) error {
	if cfg == nil || r.messenger != nil {
		return nil
	}
	adapters := make(map[string]messenger.Adapter, len(cfg.Adapters)+1)
	for platform, adapter := range cfg.Adapters {
		adapters[platform] = adapter
	}
	webexAdapter, err := webex.New(webex.Config{
		Person:    r.person,
		Vault:     r.vault,
		BaseURL:   cfg.WebexBaseURL,
		Transport: cfg.WebexTransport,
		Interval:  cfg.WebexInterval,
	})
	if err != nil {
		return err
	}
	adapters[webex.Platform] = webexAdapter
	store := messenger.Open(r.db, r.materials, r.removals)
	service := messenger.NewService(store, adapters, cfg.Interval)
	r.messengerStore, r.messengerWebex = store, webexAdapter
	runCtx, stop := context.WithCancel(ctx)
	r.messenger, r.messengerStop, r.messengerDone = service, stop, make(chan struct{})
	go func() {
		defer close(r.messengerDone)
		service.Run(runCtx)
	}()
	return nil
}

// MessengerService is the running source loop, when Core started one.
func (r *conversationRuntime) MessengerService() *messenger.Service { return r.messenger }

// Messenger answers the person's control requests for messenger sources:
// configuring the person's own Webex integration, signing in through the client
// relay, listing the sources and disconnecting one.
func (r *conversationRuntime) Messenger(ctx context.Context, session clientsrv.Session, p protocol.MessengerRequest, relay *clientsrv.SignInRelay) (clientsrv.Result, error) {
	answer := protocol.MessengerResult{Operation: p.Operation}
	effect := protocol.EffectNone
	if *p.Operation != "list" {
		effect = protocol.EffectApplied
	}
	done := clientsrv.Result{Kind: protocol.KindMessengerResult, Payload: answer, Effect: effect}
	if p.Operation == nil {
		return done, errMessengerMalformed
	}
	platform := webex.Platform
	if p.Platform != nil {
		platform = *p.Platform
	}
	if platform != webex.Platform {
		return done, errMessengerUnsupported
	}
	switch *p.Operation {
	case "list":
		if r.messengerStore == nil {
			return done, errMessengerUnavailable
		}
		sources, err := r.messengerStore.Sources(ctx, r.person)
		if err != nil {
			return done, errMessengerUnavailable
		}
		rows := make([]protocol.MessengerSource, 0, len(sources))
		for _, source := range sources {
			row := protocol.MessengerSource{Platform: source.Platform, Account: source.Account, State: string(source.State)}
			if source.Failure != "" {
				failure := source.Failure
				row.Failure = &failure
			}
			rows = append(rows, row)
		}
		answer.Sources = &rows
		done.Payload = answer
		return done, nil
	case "configure":
		if r.messengerWebex == nil {
			return done, errMessengerUnavailable
		}
		if p.ClientID == nil || p.ClientSecret == nil {
			return done, errMessengerMalformed
		}
		// The person's integration belongs to the connecting session; the
		// secret goes to the secret store and is never echoed.
		if err := r.messengerWebex.Configure(ctx, *p.ClientID, *p.ClientSecret); err != nil {
			return done, errMessengerUnavailable
		}
		status := "configured"
		answer.Status = &status
		done.Payload = answer
		return done, nil
	case "sign_in":
		if r.messengerWebex == nil {
			return done, errMessengerUnavailable
		}
		if !r.messengerWebex.Configured() {
			return done, errMessengerNotAuthorized
		}
		// The tokens and the source row are Core's; the client only relays what
		// the browser returned.
		return relay.StartExternal(session, clientsrv.ExternalSignIn{
			Begin: func(ctx context.Context, port int) (string, error) {
				return r.messengerWebex.BeginSignIn(ctx, port)
			},
			Complete: func(ctx context.Context, redirect protocol.SignInRedirect) error {
				value := func(p *string) string {
					if p == nil {
						return ""
					}
					return *p
				}
				account, err := r.messengerWebex.CompleteSignIn(ctx, webex.Redirect{
					Code: value(redirect.Code), State: value(redirect.State), Iss: value(redirect.Iss),
					Error: value(redirect.Error), ErrorDescription: value(redirect.ErrorDescription), URL: value(redirect.URL),
				})
				if err != nil {
					return err
				}
				return r.connectMessengerSource(ctx, webex.Platform, account.ID)
			},
			Disconnect: func() { r.messengerWebex.AbandonSignIn() },
			Kind:       protocol.KindMessengerResult,
			Result:     protocol.MessengerResult{Operation: p.Operation, Platform: &platform, Status: new("connected")},
		})
	case "disconnect":
		if r.messengerStore == nil || r.messengerWebex == nil {
			return done, errMessengerUnavailable
		}
		if p.Account == nil {
			return done, errMessengerMalformed
		}
		if err := r.messengerStore.Disconnect(ctx, r.person, platform, *p.Account); err != nil {
			if errors.Is(err, messenger.ErrNotFound) {
				return done, errMessengerNotFound
			}
			return done, errMessengerUnavailable
		}
		if err := r.messengerWebex.Forget(); err != nil {
			return done, errMessengerUnavailable
		}
		status := "disconnected"
		answer.Status = &status
		done.Payload = answer
		return done, nil
	}
	return done, errMessengerMalformed
}

// connectMessengerSource records the source the person just signed in to. The
// sign-in is the person's act; this is its durable record, not a discovery.
func (r *conversationRuntime) connectMessengerSource(ctx context.Context, platform, account string) error {
	source := messenger.Source{
		Person:      r.person,
		Platform:    platform,
		Account:     account,
		Mode:        messenger.ModeOfficial,
		Adapter:     platform,
		State:       messenger.StateConnected,
		ConnectedAt: time.Now(),
	}
	return r.messengerStore.Connect(ctx, source)
}

var (
	errMessengerMalformed     = &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	errMessengerUnsupported   = &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	errMessengerUnavailable   = &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	errMessengerNotAuthorized = &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	errMessengerNotFound      = &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
)
