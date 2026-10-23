// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

var SignInCapability = protocol.Capability{Name: "sign_in_relay", Version: "1.0"}
var AccountsCapability = protocol.Capability{Name: "accounts", Version: "1.0"}

type SignInService interface {
	Begin(context.Context, provider.ProviderID, accounts.RelayCaps) (accounts.Pending, error)
	Complete(context.Context, string, accounts.Relayed) (accounts.Account, error)
	Disconnect(string)
}

type relayAttempt struct {
	selection protocol.AccountRequest
	pending   string
	expires   time.Time
}
type SignInRelay struct {
	mu       sync.Mutex
	person   string
	service  SignInService
	attempts map[string]relayAttempt
	external map[string]ExternalSignIn
}

func NewSignInRelay(person string, service SignInService) *SignInRelay {
	return &SignInRelay{person: person, service: service, attempts: make(map[string]relayAttempt)}
}
func (s *SignInRelay) Operations() map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{
		protocol.KindAccountRequest:  {Capability: AccountsCapability, Handle: s.handle, Disconnect: s.disconnect},
		protocol.KindSignInListening: {Capability: SignInCapability, Handle: s.handle},
		protocol.KindSignInRedirect:  {Capability: SignInCapability, Handle: s.handle},
	}
}
func (s *SignInRelay) disconnect(session Session) {
	s.mu.Lock()
	delete(s.attempts, session.ConnectionID)
	external, ok := s.external[session.ConnectionID]
	delete(s.external, session.ConnectionID)
	s.mu.Unlock()
	if ok {
		external.Disconnect()
	}
	s.service.Disconnect(session.ConnectionID)
}
func relayRefusal(code protocol.ErrorCode) error { return &OperationError{Code: code} }
func (s *SignInRelay) handle(ctx context.Context, session Session, envelope protocol.Envelope) (Result, error) {
	if s.service == nil || session.ConnectionID == "" || session.PersonID != s.person {
		return Result{}, relayRefusal(protocol.ErrorNotAuthorized)
	}
	if _, accountRequest := envelope.Payload.(protocol.AccountRequest); !accountRequest {
		if result, handled, err := s.handleExternal(ctx, session, envelope.Payload); handled {
			return result, err
		}
	}
	if !hasCapability(session.Capabilities, AccountsCapability) || !hasCapability(session.Capabilities, SignInCapability) {
		return Result{}, relayRefusal(protocol.ErrorCapabilityNotNegotiated)
	}
	switch payload := envelope.Payload.(type) {
	case protocol.AccountRequest:
		if payload.Operation == nil || *payload.Operation != "sign_in" || payload.Provider == nil || *payload.Provider != "chatgpt" || payload.Purpose == nil || (*payload.Purpose != "login" && *payload.Purpose != "provider") {
			return Result{}, relayRefusal(protocol.ErrorMalformed)
		}
		s.disconnect(session)
		s.mu.Lock()
		for id, attempt := range s.attempts {
			if !time.Now().Before(attempt.expires) {
				delete(s.attempts, id)
				s.service.Disconnect(id)
			}
		}
		if len(s.attempts) >= 64 {
			s.mu.Unlock()
			return Result{}, relayRefusal(protocol.ErrorUnavailable)
		}
		s.attempts[session.ConnectionID] = relayAttempt{selection: payload, expires: time.Now().Add(10 * time.Minute)}
		s.mu.Unlock()
		return Result{Kind: protocol.KindSignInListen, Payload: protocol.SignInListen{}}, nil
	case protocol.SignInListening:
		s.mu.Lock()
		attempt, ok := s.attempts[session.ConnectionID]
		s.mu.Unlock()
		if !ok || attempt.pending != "" || !time.Now().Before(attempt.expires) || (payload.Port == nil) == (payload.CannotListen == nil) {
			return Result{}, relayRefusal(protocol.ErrorMalformed)
		}
		caps := accounts.RelayCaps{ConnectionID: session.ConnectionID, Purpose: accounts.SignInPurpose(*attempt.selection.Purpose)}
		if attempt.selection.AccountID != nil {
			caps.AccountID = provider.AccountID(*attempt.selection.AccountID)
		}
		if payload.Port != nil {
			port, err := payload.Port.Int64()
			if err != nil || port < 1 || port > 65535 {
				return Result{}, relayRefusal(protocol.ErrorMalformed)
			}
			caps.CanListen = true
			caps.Port = int(port)
		}
		if payload.CannotListen != nil && *payload.CannotListen == "" {
			return Result{}, relayRefusal(protocol.ErrorMalformed)
		}
		pending, err := s.service.Begin(ctx, provider.ChatGPT, caps)
		if err != nil {
			s.disconnect(session)
			return Result{}, signInError(err)
		}
		attempt.pending, attempt.expires = pending.ID, pending.Expires
		s.mu.Lock()
		s.attempts[session.ConnectionID] = attempt
		s.mu.Unlock()
		return Result{Kind: protocol.KindSignInOpen, Payload: protocol.SignInOpen{URL: &pending.URL}}, nil
	case protocol.SignInRedirect:
		s.mu.Lock()
		attempt, ok := s.attempts[session.ConnectionID]
		delete(s.attempts, session.ConnectionID)
		s.mu.Unlock()
		if !ok || attempt.pending == "" || !time.Now().Before(attempt.expires) {
			s.service.Disconnect(session.ConnectionID)
			return Result{}, relayRefusal(protocol.ErrorMalformed)
		}
		value := func(p *string) string {
			if p == nil {
				return ""
			}
			return *p
		}
		in := accounts.Relayed{ConnectionID: session.ConnectionID, Code: value(payload.Code), State: value(payload.State), Issuer: value(payload.Iss), Error: value(payload.Error), ErrorDescription: value(payload.ErrorDescription), URL: value(payload.URL)}
		saved, err := s.service.Complete(ctx, attempt.pending, in)
		if err != nil {
			return Result{}, signInError(err)
		}
		id, status, operation := string(saved.ID), string(saved.Status), "sign_in"
		return Result{Kind: protocol.KindAccountResult, Payload: protocol.AccountResult{AccountID: &id, Status: &status, Operation: &operation}, Effect: protocol.EffectApplied}, nil
	default:
		return Result{}, relayRefusal(protocol.ErrorMalformed)
	}
}
func hasCapability(values []protocol.Capability, want protocol.Capability) bool {
	for _, value := range values {
		if protocol.CapabilityIdentity(value, want) {
			return true
		}
	}
	return false
}
func signInError(err error) error {
	switch {
	case errors.Is(err, accounts.ErrOwnerMismatch), errors.Is(err, accounts.ErrSignInIdentity), errors.Is(err, accounts.ErrSignInDenied), errors.Is(err, accounts.ErrSignInScope):
		return relayRefusal(protocol.ErrorNotAuthorized)
	case errors.Is(err, accounts.ErrSignInInvalid), errors.Is(err, accounts.ErrSignInExpired), errors.Is(err, accounts.ErrSignInRestart):
		return relayRefusal(protocol.ErrorMalformed)
	default:
		return relayRefusal(protocol.ErrorUnavailable)
	}
}
