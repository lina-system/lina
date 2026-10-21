// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

type AccountService interface {
	List(context.Context, string) ([]accounts.Account, error)
	SignOut(context.Context, string, provider.AccountID) error
}

// AccountOperations extends the existing relay; it creates no alternate login
// path or store. Both management operations use the connection's person.
func AccountOperations(person string, service AccountService, relay *SignInRelay) map[protocol.Kind]Operation {
	operations := relay.Operations()
	operation := operations[protocol.KindAccountRequest]
	signIn := operation.Handle
	operation.Handle = func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.AccountRequest)
		if !ok || p.Operation == nil {
			return Result{}, relayRefusal(protocol.ErrorMalformed)
		}
		if *p.Operation == "sign_in" {
			return signIn(ctx, session, e)
		}
		if session.PersonID != person {
			return Result{}, relayRefusal(protocol.ErrorNotAuthorized)
		}
		if p.Provider != nil || p.Purpose != nil {
			return Result{}, relayRefusal(protocol.ErrorMalformed)
		}
		switch *p.Operation {
		case "list":
			if p.AccountID != nil {
				return Result{}, relayRefusal(protocol.ErrorMalformed)
			}
			rows, err := service.List(ctx, session.PersonID)
			if err != nil {
				return Result{}, relayRefusal(protocol.ErrorUnavailable)
			}
			items := []protocol.AccountSummary{}
			for _, a := range rows {
				item := protocol.AccountSummary{AccountID: string(a.ID), Provider: string(a.Provider), Label: a.Label, Status: string(a.Status)}
				if a.Plan != nil {
					item.Plan = &a.Plan.Name
				}
				items = append(items, item)
			}
			return Result{Kind: protocol.KindAccountResult, Payload: protocol.AccountResult{Operation: p.Operation, Accounts: &items}}, nil
		case "sign_out":
			if p.AccountID == nil {
				return Result{}, relayRefusal(protocol.ErrorMalformed)
			}
			if err := service.SignOut(ctx, session.PersonID, provider.AccountID(*p.AccountID)); err != nil {
				if errors.Is(err, accounts.ErrNotFound) {
					return Result{}, relayRefusal(protocol.ErrorOutOfScope)
				}
				return Result{}, relayRefusal(protocol.ErrorUnavailable)
			}
			status := "needs_sign_in"
			return Result{Kind: protocol.KindAccountResult, Effect: protocol.EffectApplied, Payload: protocol.AccountResult{Operation: p.Operation, AccountID: p.AccountID, Status: &status}}, nil
		}
		return Result{}, relayRefusal(protocol.ErrorMalformed)
	}
	operations[protocol.KindAccountRequest] = operation
	return operations
}
