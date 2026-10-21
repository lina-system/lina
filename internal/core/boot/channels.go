// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/gateway/telegram"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/secret"
)

type channelWorker struct {
	cancel  context.CancelFunc
	done    chan struct{}
	healthy *atomic.Bool
}
type channelRuntime struct {
	r         *conversationRuntime
	store     *channels.Store
	epoch     uint64
	transport http.RoundTripper
	mu        sync.Mutex
	workers   map[string]channelWorker
	closed    bool
}

func startChannels(r *conversationRuntime, epoch uint64, transport http.RoundTripper) (*channelRuntime, error) {
	c := &channelRuntime{r: r, store: channels.New(r.db), epoch: epoch, transport: transport, workers: map[string]channelWorker{}}
	rows, err := c.store.Connections(r.ctx, r.person)
	if err != nil {
		return nil, err
	}
	if err = c.store.Recover(r.ctx); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Platform == "telegram" {
			_ = c.start(row.Bot)
		}
	}
	return c, nil
}
func (c *channelRuntime) key(b channels.Bot) secrets.Key {
	return secrets.Key{Person: c.r.person, Service: "telegram", Name: b.Account}
}
func (c *channelRuntime) adapter(b channels.Bot, token secret.Secret) (*telegram.Adapter, error) {
	return telegram.New(telegram.Config{Bot: b, Token: token, DB: c.r.db, Epoch: c.epoch, Transport: c.transport})
}

// The adapter handles pairing before ordinary inbox admission. Bound messages
// remain durable in the gateway inbox for the channel turn coordinator.
func (c *channelRuntime) start(b channels.Bot) error {
	if w, ok := c.workers[b.Key()]; ok {
		select {
		case <-w.done:
			delete(c.workers, b.Key())
		default:
			return nil
		}
	}
	token, err := c.r.vault.Get(c.key(b))
	if err != nil {
		return err
	}
	adapter, err := c.adapter(b, token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(c.r.ctx)
	worker := channelWorker{cancel: cancel, done: make(chan struct{}), healthy: &atomic.Bool{}}
	c.workers[b.Key()] = worker
	go func() {
		defer close(worker.done)
		defer adapter.Close()
		superviseChannel(ctx, worker.healthy, func(ctx context.Context) error {
			if err := adapter.Identify(ctx); err != nil {
				return err
			}
			worker.healthy.Store(true)
			return adapter.Run(ctx, gateway.New(c.r.db, func(context.Context, gateway.Delivery) error { return nil }))
		}, channelPause)
	}()
	return nil
}
func (c *channelRuntime) stop(b channels.Bot) {
	if w, ok := c.workers[b.Key()]; ok {
		w.cancel()
		<-w.done
		delete(c.workers, b.Key())
	}
}
func (c *channelRuntime) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for key, w := range c.workers {
		w.cancel()
		<-w.done
		delete(c.workers, key)
	}
}
func channelError(err error) error {
	if err == nil {
		return nil
	}
	code := protocol.ErrorUnavailable
	if errors.Is(err, channels.ErrDenied) {
		code = protocol.ErrorNotAuthorized
	}
	if errors.Is(err, channels.ErrInvalid) {
		code = protocol.ErrorMalformed
	}
	return &clientsrv.OperationError{Code: code}
}
func (c *channelRuntime) Channel(ctx context.Context, s clientsrv.Session, p protocol.ChannelRequest) (protocol.ChannelResult, error) {
	if err := c.r.authorize(s, nil); err != nil {
		return protocol.ChannelResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return protocol.ChannelResult{}, channelError(secrets.ErrClosed)
	}
	result := protocol.ChannelResult{Operation: p.Operation}
	if p.Operation == nil {
		return result, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	if *p.Operation != "list" && !s.CanDecide {
		return result, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	b := channels.Bot{Platform: "telegram"}
	if p.Account != nil {
		b.Account = *p.Account
	}
	var err error
	switch *p.Operation {
	case "list":
	case "connect":
		if p.Token == nil {
			return result, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		b.Account, _, _ = strings.Cut(*p.Token, ":")
		if _, err = c.adapter(b, secret.New(*p.Token)); err != nil {
			return result, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		if err = c.store.Connect(ctx, b, c.r.person); err == nil {
			c.stop(b)
			if err = c.r.vault.Put(c.key(b), []byte(*p.Token)); err == nil {
				err = c.start(b)
			}
		}
	case "pair":
		if err = c.start(b); err == nil {
			now := time.Now()
			var code string
			code, err = c.store.PairingCode(ctx, b, c.r.person, now)
			if err == nil {
				expires := now.Add(channels.PairingLifetime).Format(time.RFC3339)
				result.PairingCode = &code
				result.ExpiresAt = &expires
			}
		}
	case "bind":
		if p.AgentID == nil || p.Scope == nil {
			return result, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		rows, e := c.store.Connections(ctx, c.r.person)
		if e != nil {
			return result, channelError(e)
		}
		chat := ""
		for _, row := range rows {
			if row.Bot == b {
				chat = row.PrivateChat
			}
		}
		if p.Chat != nil {
			chat = *p.Chat
		}
		topic := ""
		if p.Topic != nil {
			topic = *p.Topic
		}
		address := channels.Address{Bot: b, Chat: chat, Topic: topic}
		scope := channels.Scope{Reads: channels.Reads(p.Scope.Reads), Writes: channels.Writes(p.Scope.Writes), Tools: p.Scope.Tools, Memory: p.Scope.Memory}
		err = c.store.Bind(ctx, channels.Binding{Address: address, Agent: *p.AgentID, Person: c.r.person, Conversation: "channel:" + address.Key(), Scope: scope})
	case "unbind":
		if p.Chat == nil {
			return result, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		address := channels.Address{Bot: b, Chat: *p.Chat}
		if p.Topic != nil {
			address.Topic = *p.Topic
		}
		err = c.store.Unbind(ctx, address, c.r.person)
	case "disconnect":
		c.stop(b)
		if err = c.store.Disconnect(ctx, b, c.r.person); err == nil {
			err = c.r.vault.Delete(c.key(b))
			if errors.Is(err, secrets.ErrNotFound) {
				err = nil
			}
		}
	default:
		return result, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	if err != nil {
		return result, channelError(err)
	}
	rows, err := c.store.Connections(ctx, c.r.person)
	if err != nil {
		return result, channelError(err)
	}
	resultRows := []protocol.ChannelConnection{}
	for _, row := range rows {
		status := "needs_pairing"
		if row.Sender != "" {
			status = "connected"
		}
		if w, ok := c.workers[row.Bot.Key()]; !ok || !w.healthy.Load() {
			status = "unavailable"
		}
		if _, err := c.r.vault.Get(c.key(row.Bot)); err != nil {
			status = "disconnected"
		}
		bindings := []protocol.ChannelBinding{}
		for _, b := range row.Bindings {
			bindings = append(bindings, protocol.ChannelBinding{AgentID: b.Agent, Chat: b.Chat, Topic: b.Topic, Unreachable: b.Unreachable, Scope: protocol.ChannelScope{Reads: string(b.Scope.Reads), Writes: string(b.Scope.Writes), Tools: b.Scope.Tools, Memory: b.Scope.Memory}})
		}
		resultRows = append(resultRows, protocol.ChannelConnection{Platform: row.Platform, Account: row.Account, Status: status, PrivateChat: row.PrivateChat, Bindings: bindings})
	}
	result.Connections = &resultRows
	return result, nil
}

func channelPause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func superviseChannel(ctx context.Context, healthy *atomic.Bool, run func(context.Context) error, pause func(context.Context, time.Duration) error) {
	defer healthy.Store(false)
	backoff := time.Second
	for ctx.Err() == nil {
		err := run(ctx)
		healthy.Store(false)
		delay, retry := telegram.RecoveryDelay(err)
		if !retry || ctx.Err() != nil || pause(ctx, max(delay, backoff)) != nil {
			return
		}
		backoff = min(30*time.Second, backoff*2)
	}
}
