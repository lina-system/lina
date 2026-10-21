// SPDX-License-Identifier: Apache-2.0

// Package telegram implements the outbound-only Telegram gateway adapter.
package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

// Config is supplied by main's composition root. Token comes from the owner's
// secret store; Bot.Account is Telegram's numeric bot ID, not its token. The
// caller installs channels.Schema and Schema before constructing this adapter.
// Transport is an HTTP transport seam; nil uses a private standard transport.
type Config struct {
	Bot       channels.Bot
	Token     secret.Secret
	DB        channels.Canon
	Epoch     uint64
	Approvals *authority.Service
	Username  string
	Transport http.RoundTripper
}

type Adapter struct {
	bot       channels.Bot
	db        channels.Canon
	store     *channels.Store
	epoch     uint64
	approvals *authority.Service
	username  string
	client    *client
}

var _ gateway.Adapter = (*Adapter)(nil)
var pollers sync.Map

func New(cfg Config) (*Adapter, error) {
	if cfg.Bot.Platform != "telegram" || cfg.DB == nil || cfg.Epoch == 0 {
		return nil, channels.ErrInvalid
	}
	h := make(http.Header)
	if secret.InjectHeader(h, "Authorization", "", cfg.Token) != nil {
		return nil, errCredential
	}
	raw := h.Get("Authorization")
	account, _, ok := strings.Cut(raw, ":")
	id, err := strconv.ParseInt(account, 10, 64)
	if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != account || account != cfg.Bot.Account || !tokenPattern.MatchString(raw) || len(raw) > 256 {
		return nil, errCredential
	}
	c, err := newClient(cfg.Token, cfg.Transport)
	if err != nil {
		return nil, err
	}
	return &Adapter{bot: cfg.Bot, db: cfg.DB, store: channels.New(cfg.DB), epoch: cfg.Epoch, approvals: cfg.Approvals, username: strings.TrimPrefix(cfg.Username, "@"), client: c}, nil
}

func (a *Adapter) Caps() gateway.Capabilities {
	return gateway.Capabilities{TextLength: 4096, Edit: true, Buttons: true, Files: true, Threads: true, Streaming: false, ResultTypes: []string{"text"}}
}

func (a *Adapter) Run(ctx context.Context, in gateway.Admitter) error {
	if in == nil {
		return channels.ErrInvalid
	}
	if _, loaded := pollers.LoadOrStore(a.bot.Key(), struct{}{}); loaded {
		return channels.ErrConflict
	}
	defer pollers.Delete(a.bot.Key())
	defer a.client.http.CloseIdleConnections()
	previous, err := a.store.Cursor(ctx, a.bot)
	if err != nil {
		return errAdmission
	}
	offset := int64(0)
	if previous != "" {
		offset, err = strconv.ParseInt(previous, 10, 64)
		if err != nil || offset < 0 || strconv.FormatInt(offset, 10) != previous {
			return errAdmission
		}
	}
	if err := a.resumeButtons(ctx); err != nil {
		return err
	}
	return a.runUpdates(ctx, in, previous, offset)
}

func (a *Adapter) destination(address channels.Address) (int64, int64, error) {
	if address.Bot != a.bot {
		return 0, 0, channels.ErrDenied
	}
	chat, err := strconv.ParseInt(address.Chat, 10, 64)
	if err != nil || chat == 0 || strconv.FormatInt(chat, 10) != address.Chat {
		return 0, 0, channels.ErrInvalid
	}
	thread := int64(0)
	if address.Topic != "" {
		thread, err = strconv.ParseInt(address.Topic, 10, 64)
		if err != nil || thread <= 0 || strconv.FormatInt(thread, 10) != address.Topic {
			return 0, 0, channels.ErrInvalid
		}
	}
	return chat, thread, nil
}

func (a *Adapter) Send(ctx context.Context, l lease.Lease, o gateway.Outbound) (result gateway.SendResult, err error) {
	chat, thread, err := a.destination(o.Address)
	if err != nil {
		return result, err
	}
	err = lease.Do(ctx, l, func(ctx context.Context) error {
		id, callErr := a.client.sendText(ctx, chat, thread, o.Text, nil, true)
		result, callErr = sendResult(id, callErr)
		return callErr
	})
	return result, err
}

func sendResult(id int64, err error) (gateway.SendResult, error) {
	if err == nil {
		return gateway.SendResult{Outcome: gateway.Delivered, MessageID: strconv.FormatInt(id, 10)}, nil
	}
	var f *failure
	if errors.As(err, &f) {
		outcome := gateway.Uncertain
		switch f.class {
		case "retry_later":
			outcome = gateway.RetryLater
		case "blocked":
			outcome = gateway.PermanentlyBlocked
		case "too_long":
			outcome = gateway.TooLong
		case "unauthorized":
			outcome = gateway.Unauthorized
		}
		return gateway.SendResult{Outcome: outcome, RetryAfter: f.retryAfter}, nil
	}
	return gateway.SendResult{Outcome: gateway.Uncertain}, err
}

func (a *Adapter) Edit(ctx context.Context, ref gateway.MsgRef, o gateway.Outbound) error {
	chat, _, err := a.destination(ref.Address)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil || id <= 0 {
		return channels.ErrInvalid
	}
	return a.client.editText(ctx, chat, id, o.Text, nil, true)
}
func (a *Adapter) Answer(ctx context.Context, ref gateway.InteractionRef, text string) error {
	if ref.Bot != a.bot || ref.ID == "" {
		return channels.ErrInvalid
	}
	return a.client.answerCallback(ctx, ref.ID, text)
}
func (a *Adapter) Fetch(ctx context.Context, ref gateway.AttachmentRef) (io.ReadCloser, error) {
	if ref.Bot != a.bot {
		return nil, channels.ErrDenied
	}
	return a.client.fetch(ctx, ref.ID)
}
