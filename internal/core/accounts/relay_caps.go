// SPDX-License-Identifier: Apache-2.0

package accounts

import (
	"context"

	"github.com/thisisjun786/lina/kit/provider"
)

type SignInPurpose string

const (
	Login       SignInPurpose = "login"
	AddProvider SignInPurpose = "provider"
)

// RelayCaps comes from the initiating client's listener report. ConnectionID
// is assigned by Core, never copied from an untrusted payload. The person and
// host identity are resolved independently by the service's durable store.
type RelayCaps struct {
	ConnectionID string
	CanListen    bool
	Port         int
	Purpose      SignInPurpose
	AccountID    provider.AccountID
}

// signInRegistration is a projection of a saved provider account, loaded by
// Core. A client may select its ID but cannot provide these identity fields.
type signInRegistration struct {
	accountID                                 provider.AccountID
	person, issuer, subject, clientID, hostID string
	provider                                  provider.ProviderID
}

func (s *pendingSignIns) beginRelay(ctx context.Context, p provider.ProviderID, person, hostID string, r RelayCaps, registration *signInRegistration) (Pending, error) {
	if p != provider.ChatGPT || person == "" || hostID == "" || r.ConnectionID == "" {
		return Pending{}, ErrSignInInvalid
	}
	switch r.Purpose {
	case Login, AddProvider:
	default:
		return Pending{}, ErrSignInInvalid
	}
	port := r.Port
	if !r.CanListen {
		if port != 0 {
			return Pending{}, ErrSignInInvalid
		}
		// No listener is opened in this mode. The browser's redirected URL is
		// copied by the person, then validated against this exact URI in Core.
		port = 1455
	}
	selected := signInSelection{accountID: string(r.AccountID), login: r.Purpose == Login}
	if r.AccountID != "" {
		if registration == nil || registration.accountID != r.AccountID || registration.person != person || registration.provider != p ||
			registration.issuer != openAIIssuer || registration.hostID != hostID || registration.clientID == "" || registration.subject == "" {
			return Pending{}, ErrSignInInvalid
		}
		selected.clientID, selected.subject = registration.clientID, registration.subject
	} else if registration != nil {
		return Pending{}, ErrSignInInvalid
	}
	return s.begin(ctx, r.ConnectionID, hostID, selected, port)
}
