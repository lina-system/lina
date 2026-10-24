// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"net/http"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/webex"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) initWebex(relay *clientsrv.SignInRelay, transport http.RoundTripper) error {
	store, err := r.messengerStore(r.ctx)
	if err != nil {
		return err
	}
	r.webex = webex.NewService(r.ctx, store, webex.Config{Person: r.person, Vault: r.vault, Transport: transport})
	if err := r.webex.Start(r.ctx); err != nil {
		return err
	}
	return addOperations(&r.identity, clientsrv.WebexOperations(r, relay))
}
func webexError(err error) error {
	if err == nil {
		return nil
	}
	code := protocol.ErrorUnavailable
	if errors.Is(err, messenger.ErrInvalid) || errors.Is(err, messenger.ErrNotFound) || errors.Is(err, webex.ErrMisconfigured) {
		code = protocol.ErrorMalformed
	}
	if errors.Is(err, messenger.ErrNeedsSignIn) {
		code = protocol.ErrorNotAuthorized
	}
	return &clientsrv.OperationError{Code: code}
}
func (r *conversationRuntime) Webex(ctx context.Context, s clientsrv.Session, p protocol.WebexRequest, relay *clientsrv.SignInRelay) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	if !s.CanDecide || s.Epoch != r.epoch {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if p.Operation == nil || p.Platform != nil && *p.Platform != webex.Platform {
		return clientsrv.Result{}, malformedManagement()
	}
	op := *p.Operation
	if op != "configure" && (p.ClientID != nil || p.ClientSecret != nil) || op != "disconnect" && p.Account != nil {
		return clientsrv.Result{}, malformedManagement()
	}
	out := protocol.WebexResult{Operation: p.Operation, Platform: new(webex.Platform)}
	effect := protocol.EffectNone
	var err error
	switch op {
	case "configure":
		if p.ClientID == nil || p.ClientSecret == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		err = r.webex.Configure(ctx, *p.ClientID, *p.ClientSecret)
		out.Status = new("configured")
		effect = protocol.EffectApplied
	case "sign_in":
		return relay.StartExternal(s, clientsrv.ExternalSignIn{PastePort: 8400, Begin: func(ctx context.Context, port int) (string, error) {
			return r.webex.Begin(ctx, s.ConnectionID, port)
		}, Complete: func(ctx context.Context, p protocol.SignInRedirect) error {
			if p.URL != nil && (p.Code != nil || p.State != nil || p.Iss != nil || p.Error != nil || p.ErrorDescription != nil) {
				return messenger.ErrInvalid
			}
			account, err := r.webex.Complete(ctx, s.ConnectionID, webex.Redirect{Code: value(p.Code), State: value(p.State), Iss: value(p.Iss), Error: value(p.Error), ErrorDescription: value(p.ErrorDescription), URL: value(p.URL)})
			if err == nil {
				out.Status = new("connected")
				out.Account = &account.ID
			}
			return err
		}, Disconnect: func() { r.webex.Cancel(s.ConnectionID) }, Kind: protocol.KindWebexResult, Result: &out})
	case "disconnect":
		if p.Account == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		err = r.webex.Disconnect(ctx, *p.Account)
		out.Account = p.Account
		out.Status = new("disconnected")
		effect = protocol.EffectApplied
	case "list":
		rows, loadErr := r.webex.Sources(ctx)
		err = loadErr
		sources := []protocol.WebexSource{}
		for _, row := range rows {
			source := protocol.WebexSource{Platform: row.Platform, Account: row.Account, State: string(row.State)}
			if row.Failure != "" {
				source.Failure = &row.Failure
			}
			sources = append(sources, source)
		}
		out.Sources = &sources
	default:
		return clientsrv.Result{}, malformedManagement()
	}
	if err != nil {
		return clientsrv.Result{}, webexError(err)
	}
	return clientsrv.Result{Kind: protocol.KindWebexResult, Payload: out, Effect: effect}, nil
}
