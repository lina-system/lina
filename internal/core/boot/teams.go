// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"net/http"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/teams"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) initTeams(relay *clientsrv.SignInRelay, transport http.RoundTripper) error {
	r.teams = teams.NewService(r.ctx, r.db, r.vault, messenger.Open(r.db, r.materials, r.removals), r.person, transport)
	if err := r.teams.Start(r.ctx); err != nil {
		return err
	}
	return addOperations(&r.identity, map[protocol.Kind]clientsrv.Operation{protocol.KindTeamsRequest: {Capability: protocol.Capability{Name: "teams", Version: "1.0"}, Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
		return r.teamsOperation(ctx, s, e, relay)
	}}})
}
func teamsError(err error) error {
	if err == nil {
		return nil
	}
	code := protocol.ErrorUnavailable
	if errors.Is(err, teams.ErrInvalid) || errors.Is(err, teams.ErrDestination) {
		code = protocol.ErrorMalformed
	}
	if errors.Is(err, teams.ErrSignIn) {
		code = protocol.ErrorNotAuthorized
	}
	return &clientsrv.OperationError{Code: code}
}
func teamsStatuses(rows []teams.Connection) []protocol.TeamsConnectionStatus {
	out := []protocol.TeamsConnectionStatus{}
	for _, c := range rows {
		out = append(out, protocol.TeamsConnectionStatus{ID: c.ID, Tenant: c.Tenant, ClientID: c.ClientID, Account: c.Account, Status: c.Status, Reason: c.Reason})
	}
	return out
}
func (r *conversationRuntime) teamsOperation(ctx context.Context, s clientsrv.Session, e protocol.Envelope, relay *clientsrv.SignInRelay) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	if !s.CanDecide || s.Epoch != r.epoch {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	p, ok := e.Payload.(protocol.TeamsRequest)
	if !ok {
		return clientsrv.Result{}, malformedManagement()
	}
	out := protocol.TeamsResult{Operation: p.Operation}
	effect := protocol.EffectNone
	if p.Operation != "configure" && (p.Tenant != nil || p.ClientID != nil) || p.Operation != "switches" && (p.Sync != nil || p.Learn != nil || p.Embed != nil || p.Chat != nil) {
		return clientsrv.Result{}, malformedManagement()
	}
	if p.Operation != "list" && p.ID == nil {
		return clientsrv.Result{}, malformedManagement()
	}
	var err error
	switch p.Operation {
	case "configure":
		if p.Tenant == nil || p.ClientID == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		err = r.teams.Configure(ctx, teams.Connection{ID: value(p.ID), Tenant: value(p.Tenant), ClientID: value(p.ClientID)})
		effect = protocol.EffectApplied
	case "list":
	case "disconnect":
		err = r.teams.Disconnect(ctx, value(p.ID))
		effect = protocol.EffectApplied
	case "sign_in":
		return relay.StartExternal(s, clientsrv.ExternalSignIn{PastePort: 8400, Begin: func(ctx context.Context, port int) (string, error) {
			return r.teams.Begin(ctx, value(p.ID), s.ConnectionID, port)
		}, Complete: func(ctx context.Context, p protocol.SignInRedirect) error {
			var err error
			if p.URL != nil && p.Code == nil && p.State == nil && p.Iss == nil && p.Error == nil {
				err = r.teams.CompleteURL(ctx, s.ConnectionID, *p.URL)
			} else if p.URL == nil {
				err = r.teams.Complete(ctx, s.ConnectionID, value(p.Code), value(p.State), value(p.Iss), value(p.Error))
			} else {
				return teams.ErrSignIn
			}
			if err != nil && !errors.Is(err, teams.ErrUnsupported) {
				return err
			}
			rows, loadErr := r.teams.Connections(ctx)
			if loadErr != nil {
				return loadErr
			}
			statuses := teamsStatuses(rows)
			out.Connections = &statuses
			return nil
		}, Disconnect: func() { r.teams.Cancel(s.ConnectionID) }, Kind: protocol.KindTeamsResult, Result: &out})
	case "chats", "switches":
		rows, loadErr := r.teams.Connections(ctx)
		if loadErr != nil {
			return clientsrv.Result{}, teamsError(loadErr)
		}
		account := ""
		for _, c := range rows {
			if c.ID == value(p.ID) {
				account = c.Account
			}
		}
		if account == "" {
			return clientsrv.Result{}, malformedManagement()
		}
		store := messenger.Open(r.db, r.materials, r.removals)
		if p.Operation == "switches" {
			if p.Chat == nil || p.Sync == nil || p.Learn == nil || p.Embed == nil {
				return clientsrv.Result{}, malformedManagement()
			}
			err = store.SetSwitches(ctx, s.PersonID, "teams", account, *p.Chat, messenger.Switches{Sync: *p.Sync, Learn: *p.Learn, Embed: *p.Embed})
			effect = protocol.EffectApplied
		}
		if err == nil {
			chats, loadErr := store.Chats(ctx, s.PersonID, "teams", account)
			err = loadErr
			statuses := []protocol.TeamsChatStatus{}
			for _, c := range chats {
				statuses = append(statuses, protocol.TeamsChatStatus{ID: c.Chat, Name: c.Name, Kind: c.Kind, Sync: c.Sync, Learn: c.Learn, Embed: c.Embed})
			}
			out.Chats = &statuses
		}
	default:
		return clientsrv.Result{}, malformedManagement()
	}
	if err != nil {
		return clientsrv.Result{}, teamsError(err)
	}
	rows, err := r.teams.Connections(ctx)
	statuses := teamsStatuses(rows)
	out.Connections = &statuses
	return clientsrv.Result{Kind: protocol.KindTeamsResult, Payload: out, Effect: effect}, teamsError(err)
}
