// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
	"time"
)

// ExternalSignIn shares the existing browser/listener relay without admitting a
// model-provider account. Core supplies the issuer-bound service implementation.
type ExternalSignIn struct {
	// PastePort is the registered loopback port for clients that cannot listen.
	PastePort  int
	Begin      func(context.Context, int) (string, error)
	Complete   func(context.Context, protocol.SignInRedirect) error
	Disconnect func()
	Result     protocol.Payload
	Kind       protocol.Kind
	expires    time.Time
}

func (s *SignInRelay) StartExternal(session Session, attempt ExternalSignIn) (Result, error) {
	if s.service == nil || attempt.Begin == nil || attempt.Complete == nil || attempt.Disconnect == nil || attempt.Result == nil || session.PersonID != s.person || !session.CanDecide || session.ConnectionID == "" || !hasCapability(session.Capabilities, SignInCapability) {
		return Result{}, relayRefusal(protocol.ErrorNotAuthorized)
	}
	s.disconnect(session)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.external == nil {
		s.external = map[string]ExternalSignIn{}
	}
	if len(s.external) >= 64 {
		return Result{}, relayRefusal(protocol.ErrorUnavailable)
	}
	attempt.expires = time.Now().Add(10 * time.Minute)
	s.external[session.ConnectionID] = attempt
	return Result{Kind: protocol.KindSignInListen, Payload: protocol.SignInListen{}}, nil
}
func (s *SignInRelay) handleExternal(ctx context.Context, session Session, p protocol.Payload) (Result, bool, error) {
	s.mu.Lock()
	attempt, ok := s.external[session.ConnectionID]
	s.mu.Unlock()
	if !ok {
		return Result{}, false, nil
	}
	if session.PersonID != s.person || !session.CanDecide || !hasCapability(session.Capabilities, SignInCapability) || !time.Now().Before(attempt.expires) {
		s.disconnect(session)
		return Result{}, true, relayRefusal(protocol.ErrorNotAuthorized)
	}
	fail := func() (Result, bool, error) {
		s.disconnect(session)
		return Result{}, true, relayRefusal(protocol.ErrorUnavailable)
	}
	switch p := p.(type) {
	case protocol.SignInListening:
		if attempt.Begin == nil {
			return fail()
		}
		var port int64
		var err error
		if p.Port != nil && p.CannotListen == nil {
			port, err = p.Port.Int64()
		} else if p.Port == nil && p.CannotListen != nil && *p.CannotListen != "" {
			port = int64(attempt.PastePort)
		}
		if err != nil || port < 1 || port > 65535 {
			return fail()
		}
		url, err := attempt.Begin(ctx, int(port))
		if err != nil {
			return fail()
		}
		attempt.Begin = nil
		s.mu.Lock()
		s.external[session.ConnectionID] = attempt
		s.mu.Unlock()
		return Result{Kind: protocol.KindSignInOpen, Payload: protocol.SignInOpen{URL: &url}}, true, nil
	case protocol.SignInRedirect:
		s.mu.Lock()
		delete(s.external, session.ConnectionID)
		s.mu.Unlock()
		defer attempt.Disconnect()
		if err := attempt.Complete(ctx, p); err != nil {
			return Result{}, true, relayRefusal(protocol.ErrorNotAuthorized)
		}
		return Result{Kind: attempt.Kind, Payload: attempt.Result, Effect: protocol.EffectApplied}, true, nil
	default:
		return fail()
	}
}
