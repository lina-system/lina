// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

type shellSignInExpired struct{ Generation, Attempt uint64 }
type shellBrowserResult struct {
	Generation, Attempt uint64
	Failed              bool
}

type signInSession struct {
	ctx      context.Context
	flowCtx  context.Context
	model    *shellModel
	publish  func(context.Context, tea.Msg)
	remote   bool
	open     func(context.Context, string) error
	peer     *clientapi.Client
	pending  map[string]protocol.Kind
	attempt  uint64
	cancel   context.CancelFunc
	listener *clientapi.SignInListener
	workers  sync.WaitGroup
}

func (s *signInSession) close() {
	s.attempt++
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	s.workers.Wait()
	clear(s.pending)
}

func (s *signInSession) request(kind protocol.Kind, payload protocol.Payload) error {
	var id string
	var err error
	switch p := payload.(type) {
	case protocol.AccountRequest:
		id, err = s.peer.Account(s.ctx, p)
	case protocol.SignInListening:
		id, err = s.peer.SignInListening(s.ctx, p)
	case protocol.SignInRedirect:
		id, err = s.peer.SignInRedirect(s.ctx, p)
	default:
		return clientapi.ErrMalformed
	}
	if err == nil {
		s.pending[id] = kind
	}
	return err
}

func (s *signInSession) start(request protocol.AccountRequest) error {
	s.close()
	s.pending = make(map[string]protocol.Kind)
	s.flowCtx, s.cancel = context.WithCancel(s.ctx)
	return s.request(protocol.KindAccountRequest, request)
}

func (s *signInSession) listen() error {
	ctx := s.flowCtx
	generation, attempt := s.model.generation, s.attempt
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		timer := time.NewTimer(10 * time.Minute)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			s.publish(ctx, shellSignInExpired{generation, attempt})
		}
	}()
	reason := "remote_terminal"
	if !s.remote {
		listener, err := clientapi.ListenSignIn(ctx)
		if err == nil {
			s.listener = listener
			port := json.Number(strconv.Itoa(listener.Port()))
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				select {
				case redirect := <-listener.Redirects():
					s.publish(ctx, shellRedirect{generation, attempt, redirect})
				case <-ctx.Done():
				}
			}()
			return s.request(protocol.KindSignInListening, protocol.SignInListening{Port: &port})
		}
		reason = "listener_unavailable"
	}
	return s.request(protocol.KindSignInListening, protocol.SignInListening{CannotListen: &reason})
}

// response consumes only replies to this session's requests, before the generic
// reducer sees them. Authorization URLs and callbacks never enter its history.
func (s *signInSession) response(e protocol.Envelope) (bool, error) {
	if e.ReplyTo == nil {
		return false, nil
	}
	kind, ok := s.pending[*e.ReplyTo]
	if !ok {
		return false, nil
	}
	delete(s.pending, *e.ReplyTo)
	if e.Error != nil {
		s.close()
		s.model.accounts = accountView{open: true, code: string(e.Error.Code)}
		return true, nil
	}
	switch p := e.Payload.(type) {
	case protocol.SignInListen:
		if kind != protocol.KindAccountRequest {
			break
		}
		return true, s.listen()
	case protocol.SignInOpen:
		if kind != protocol.KindSignInListening || p.URL == nil || !browserURL(*p.URL) {
			break
		}
		s.model.accounts = accountView{open: true, stage: "sign_in_open", url: *p.URL}
		if !s.remote {
			generation, attempt, target := s.model.generation, s.attempt, *p.URL
			ctx, cancel := context.WithTimeout(s.flowCtx, 10*time.Second)
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer cancel()
				s.publish(ctx, shellBrowserResult{generation, attempt, s.open(ctx, target) != nil})
			}()
		}
		return true, nil
	case protocol.AccountResult:
		if p.Operation == nil {
			break
		}
		switch *p.Operation {
		case "list":
			if kind != protocol.KindAccountRequest || p.Accounts == nil {
				break
			}
			s.close()
			s.model.accountRows = *p.Accounts
			s.model.accountsKnown = true
			s.model.accounts = accountView{open: s.model.accounts.open}
			return true, nil
		case "sign_in", "sign_out":
			if p.Status == nil || (*p.Operation == "sign_in" && kind != protocol.KindSignInRedirect) || (*p.Operation == "sign_out" && kind != protocol.KindAccountRequest) {
				break
			}
			s.close()
			s.model.accounts = accountView{open: true, busy: true}
			return true, s.start(protocol.AccountRequest{Operation: stringPointer("list")})
		}
	}
	s.close()
	s.model.accounts = accountView{open: true, code: "malformed"}
	return true, nil
}

func (s *signInSession) handle(message tea.Msg) (bool, error) {
	switch msg := message.(type) {
	case shellAccountRequest:
		if msg.Generation != s.model.generation || s.peer == nil {
			return true, nil
		}
		return true, s.start(msg.Request)
	case shellRedirect:
		if msg.Generation != s.model.generation || (msg.Attempt != 0 && msg.Attempt != s.attempt) || s.model.accounts.stage != "sign_in_open" && !(msg.Attempt == 0 && s.model.accounts.stage == "sign_in_redirect") {
			return true, nil
		}
		authorizationURL := s.model.accounts.url
		s.model.accounts = accountView{open: true, stage: "sign_in_redirect", busy: true}
		if msg.Request.URL != nil && !callbackURL(*msg.Request.URL) {
			s.model.accounts = accountView{open: true, stage: "sign_in_open", code: "malformed", url: authorizationURL}
			return true, nil
		}
		return true, s.request(protocol.KindSignInRedirect, msg.Request)
	case shellCancelSignIn:
		if msg.Generation == s.model.generation {
			active := s.cancel != nil || len(s.pending) > 0
			s.close()
			if active && s.peer != nil {
				s.peer.Close()
			} // Core cancels this connection's attempt.
		}
		return true, nil
	case shellSignInExpired:
		if msg.Generation == s.model.generation && msg.Attempt == s.attempt {
			s.close()
			s.model.accounts = accountView{open: true, code: "account.expired"}
		}
		return true, nil
	case shellBrowserResult:
		// Browser failure leaves the same URL and pasted callback route available.
		return true, nil
	}
	return false, nil
}

func browserURL(raw string) bool { return clientapi.SignInURLAllowed(raw) }
func openSignInBrowser(ctx context.Context, raw string) error {
	return clientapi.OpenSignInBrowser(ctx, raw)
}

func remoteTerminal() bool { return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" }

func callbackURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && u.Host != "" && u.User == nil && len(raw) <= 16384
}
