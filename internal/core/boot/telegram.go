// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/telegram"
	"github.com/thisisjun786/lina/kit/protocol"
)

type telegramStep struct {
	stage string
	err   error
}
type telegramAttempt struct {
	cancel  context.CancelFunc
	done    chan struct{}
	answers chan string
	steps   chan telegramStep
}
type telegramControl struct {
	r        *conversationRuntime
	mu       sync.Mutex
	attempts map[string]*telegramAttempt
	factory  messenger.Factory
}

func (r *conversationRuntime) initTelegram() error {
	c := &telegramControl{r: r, attempts: map[string]*telegramAttempt{}, factory: r.messengerFactory(nil)}
	r.telegram = c
	return addOperations(&r.identity, map[protocol.Kind]clientsrv.Operation{
		protocol.KindTelegramRequest: {Capability: protocol.Capability{Name: "telegram_source", Version: "1.0"}, Handle: c.handle, Disconnect: func(s clientsrv.Session) { c.cancel(s.ConnectionID) }},
	})
}
func (c *telegramControl) cancel(connection string) {
	c.mu.Lock()
	a := c.attempts[connection]
	delete(c.attempts, connection)
	c.mu.Unlock()
	if a != nil {
		a.cancel()
		<-a.done
	}
}
func (c *telegramControl) close() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.attempts))
	for id := range c.attempts {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.cancel(id)
	}
}
func telegramError(err error) error {
	if err == nil {
		return nil
	}
	code := protocol.ErrorUnavailable
	if errors.Is(err, telegram.ErrInvalid) {
		code = protocol.ErrorMalformed
	}
	return &clientsrv.OperationError{Code: code}
}
func (c *telegramControl) store() *messenger.Store {
	return messenger.Open(c.r.db, c.r.materials, c.r.removals)
}
func (c *telegramControl) handle(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := c.r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	if !s.CanDecide || s.Epoch != c.r.epoch {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	p, ok := e.Payload.(protocol.TelegramRequest)
	if !ok {
		return clientsrv.Result{}, malformedManagement()
	}
	if p.Operation != "start" && (p.APIID != nil || p.APIHash != nil || p.Phone != nil) || p.Operation != "answer" && p.Answer != nil || p.Operation != "disconnect" && p.Account != nil {
		return clientsrv.Result{}, malformedManagement()
	}
	stage := "idle"
	effect := protocol.EffectNone
	var err error
	switch p.Operation {
	case "list":
	case "cancel":
		c.cancel(s.ConnectionID)
	case "start":
		if s.ConnectionID == "" || p.APIID == nil || p.APIHash == nil || p.Phone == nil || len(*p.Phone) > 64 || len(*p.APIHash) > 256 {
			return clientsrv.Result{}, malformedManagement()
		}
		c.cancel(s.ConnectionID)
		var id int
		id, err = strconv.Atoi(*p.APIID)
		if err == nil {
			err = telegram.PutCredentials(c.r.vault, s.PersonID, id, *p.APIHash)
		}
		if err == nil {
			err = c.start(s.ConnectionID, *p.Phone)
		}
		if err == nil {
			stage, err = c.next(ctx, s.ConnectionID)
		}
	case "answer":
		if p.Answer == nil || len(*p.Answer) > 1024 {
			return clientsrv.Result{}, malformedManagement()
		}
		c.mu.Lock()
		a := c.attempts[s.ConnectionID]
		c.mu.Unlock()
		if a == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		select {
		case a.answers <- *p.Answer:
		case <-a.done:
			err = telegram.ErrInvalid
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err == nil {
			stage, err = c.next(ctx, s.ConnectionID)
		}
	case "disconnect":
		if p.Account == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		source := messenger.Source{Person: s.PersonID, Platform: telegram.Platform, Account: *p.Account}
		if c.r.messenger != nil {
			err = c.r.messenger.Disconnect(ctx, source)
		} else {
			err = c.store().Disconnect(ctx, s.PersonID, telegram.Platform, *p.Account)
		}
		if err == nil {
			err = telegram.DeleteSession(c.r.vault, s.PersonID, *p.Account)
		}
		effect = protocol.EffectApplied
	default:
		return clientsrv.Result{}, malformedManagement()
	}
	if err != nil {
		c.cancel(s.ConnectionID)
		return clientsrv.Result{}, telegramError(err)
	}
	if stage == "connected" {
		if c.r.messenger != nil {
			c.r.messenger.Wake()
		}
		effect = protocol.EffectApplied
		c.cancel(s.ConnectionID)
	}
	rows, err := c.store().Sources(ctx, s.PersonID)
	sources := []protocol.TelegramSource{}
	for _, row := range rows {
		if row.Platform == telegram.Platform {
			sources = append(sources, protocol.TelegramSource{Account: row.Account, State: string(row.State)})
		}
	}
	return clientsrv.Result{Kind: protocol.KindTelegramResult, Payload: protocol.TelegramResult{Stage: stage, Sources: sources}, Effect: effect}, telegramError(err)
}
func (c *telegramControl) next(ctx context.Context, connection string) (string, error) {
	c.mu.Lock()
	a := c.attempts[connection]
	c.mu.Unlock()
	if a == nil {
		return "", telegram.ErrInvalid
	}
	select {
	case step := <-a.steps:
		return step.stage, step.err
	case <-a.done:
		select {
		case step := <-a.steps:
			return step.stage, step.err
		default:
			return "", telegram.ErrInvalid
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (c *telegramControl) start(connection, phone string) error {
	pending := "pending:" + rand.Text()
	source := messenger.Source{Person: c.r.person, Platform: telegram.Platform, Account: pending, Adapter: telegram.Platform}
	raw, ok, err := c.factory(source)
	if err != nil {
		return err
	}
	adapter, typed := raw.(*telegram.Adapter)
	if !ok || !typed {
		return telegram.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(c.r.ctx, 5*time.Minute)
	a := &telegramAttempt{cancel: cancel, done: make(chan struct{}), answers: make(chan string), steps: make(chan telegramStep, 1)}
	c.mu.Lock()
	if len(c.attempts) >= 8 {
		c.mu.Unlock()
		cancel()
		adapter.Close()
		return telegram.ErrInvalid
	}
	c.attempts[connection] = a
	c.mu.Unlock()
	go func() {
		defer close(a.done)
		defer cancel()
		defer telegram.DeleteSession(c.r.vault, c.r.person, pending)
		defer adapter.Close()
		ask := func(stage string) (string, error) {
			select {
			case a.steps <- telegramStep{stage: stage}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case answer := <-a.answers:
				return answer, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		err := adapter.Authenticate(ctx, telegram.PromptFuncs{PhoneFunc: func(context.Context) (string, error) { return phone, nil }, CodeFunc: func(context.Context, telegram.Code) (string, error) { return ask("code") }, PasswordFunc: func(context.Context) (string, error) { return ask("password") }})
		if err == nil {
			var account string
			account, err = adapter.SaveSession(ctx)
			if err == nil {
				source.Account = account
				source.Mode = messenger.ModeOfficial
				source.State = messenger.StateConnected
				err = c.store().Connect(ctx, source)
			}
		}
		select {
		case a.steps <- telegramStep{stage: "connected", err: err}:
		case <-ctx.Done():
		}
	}()
	return nil
}
