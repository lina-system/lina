// SPDX-License-Identifier: Apache-2.0

// Package discord implements Discord's outbound Gateway and REST boundary.
package discord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

type Config struct {
	Bot       channels.Bot
	Token     secret.Secret
	DB        channels.Canon
	Epoch     uint64
	Approvals *authority.Service
	Transport http.RoundTripper
}
type Adapter struct {
	bot          channels.Bot
	db           channels.Canon
	store        *channels.Store
	epoch        uint64
	approvals    *authority.Service
	token        secret.Secret
	http         *http.Client
	mu           sync.Mutex
	interactions map[string]string
}

var _ gateway.Adapter = (*Adapter)(nil)
var snowflake = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var errUnavailable = errors.New("discord_unavailable")
var errResponse = errors.New("discord_invalid_response")
var errTooLong = errors.New("discord_too_long")
var sessions sync.Map

func New(c Config) (*Adapter, error) {
	if c.Bot.Platform != "discord" || !snowflake.MatchString(c.Bot.Account) || c.DB == nil || c.Epoch == 0 {
		return nil, channels.ErrInvalid
	}
	h := make(http.Header)
	if secret.InjectHeader(h, "Authorization", "Bot ", c.Token) != nil || len(h.Get("Authorization")) <= 4 {
		return nil, channels.ErrInvalid
	}
	tr := c.Transport
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &Adapter{bot: c.Bot, db: c.DB, store: channels.New(c.DB), epoch: c.Epoch, approvals: c.Approvals, token: c.Token, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errUnavailable }}, interactions: map[string]string{}}, nil
}
func (a *Adapter) Close() { a.http.CloseIdleConnections() }
func (a *Adapter) Caps() gateway.Capabilities {
	return gateway.Capabilities{TextLength: 2000, Edit: true, Buttons: true, Files: true, Threads: true, ResultTypes: []string{"text"}}
}
func (a *Adapter) Identify(ctx context.Context) error {
	var user struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot"`
	}
	result, err := a.request(ctx, http.MethodGet, "/users/@me", nil, &user)
	if err != nil {
		return err
	}
	if result.Outcome != gateway.Delivered || !user.Bot || user.ID != a.bot.Account {
		return errUnavailable
	}
	return nil
}

// IdentifyAccount derives identity from Discord, never from an unverified token prefix.
func IdentifyAccount(ctx context.Context, token secret.Secret, tr http.RoundTripper) (string, error) {
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	a := &Adapter{token: token, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errUnavailable }}}
	defer a.Close()
	var user struct {
		ID  string `json:"id"`
		Bot bool   `json:"bot"`
	}
	result, err := a.request(ctx, http.MethodGet, "/users/@me", nil, &user)
	if err != nil {
		return "", err
	}
	if result.Outcome != gateway.Delivered || !user.Bot || !snowflake.MatchString(user.ID) {
		return "", errUnavailable
	}
	return user.ID, nil
}
func (a *Adapter) destination(addr channels.Address) (string, error) {
	if addr.Bot != a.bot {
		return "", channels.ErrDenied
	}
	id := addr.Chat
	if addr.Topic != "" {
		id = addr.Topic
	}
	if !snowflake.MatchString(id) {
		return "", channels.ErrInvalid
	}
	return id, nil
}
func (a *Adapter) Send(ctx context.Context, l lease.Lease, o gateway.Outbound) (result gateway.SendResult, err error) {
	channel, err := a.destination(o.Address)
	if err != nil {
		return result, err
	}
	if o.Text == "" || len(o.Text) > 8<<20 {
		return gateway.SendResult{Outcome: gateway.TooLong}, nil
	}
	err = lease.Do(ctx, l, func(ctx context.Context) error { var e error; result, e = a.send(ctx, channel, o.Text, nil); return e })
	return
}
func (a *Adapter) send(ctx context.Context, channel, text string, components any) (gateway.SendResult, error) {
	body := map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}}
	if components != nil {
		body["components"] = components
	}
	var message struct {
		ID string `json:"id"`
	}
	var payload any = body
	if units(text) > 2000 {
		payload = fileMessage{Text: text}
	}
	r, err := a.request(ctx, http.MethodPost, "/channels/"+channel+"/messages", payload, &message)
	if r.Outcome == gateway.Delivered {
		if !snowflake.MatchString(message.ID) {
			return gateway.SendResult{Outcome: gateway.Uncertain}, errResponse
		}
		r.MessageID = message.ID
	}
	return r, err
}
func (a *Adapter) Edit(ctx context.Context, ref gateway.MsgRef, o gateway.Outbound) error {
	channel, err := a.destination(ref.Address)
	if err != nil {
		return err
	}
	if !snowflake.MatchString(ref.ID) || units(o.Text) > 2000 {
		return channels.ErrInvalid
	}
	r, err := a.request(ctx, http.MethodPatch, "/channels/"+channel+"/messages/"+ref.ID, map[string]any{"content": o.Text, "components": []any{}, "allowed_mentions": map[string]any{"parse": []string{}}}, nil)
	if err == nil && r.Outcome != gateway.Delivered {
		err = errUnavailable
	}
	return err
}
func (a *Adapter) Answer(ctx context.Context, ref gateway.InteractionRef, text string) error {
	if ref.Bot != a.bot || !snowflake.MatchString(ref.ID) || units(text) > 2000 {
		return channels.ErrInvalid
	}
	a.mu.Lock()
	token := a.interactions[ref.ID]
	delete(a.interactions, ref.ID)
	a.mu.Unlock()
	if token == "" {
		return channels.ErrDenied
	}
	r, err := a.request(ctx, http.MethodPost, "/interactions/"+ref.ID+"/"+token+"/callback", map[string]any{"type": 4, "data": map[string]any{"content": text, "flags": 64, "allowed_mentions": map[string]any{"parse": []string{}}}}, nil)
	if err == nil && r.Outcome != gateway.Delivered {
		err = errUnavailable
	}
	return err
}
func (a *Adapter) Fetch(context.Context, gateway.AttachmentRef) (io.ReadCloser, error) {
	return nil, channels.ErrDenied
}

func RecoveryDelay(err error) (time.Duration, bool) {
	return time.Second, errors.Is(err, errUnavailable)
}

// Guild channel metadata establishes thread ancestry. It is never inferred from
// message text or an interaction's component data.
func (a *Adapter) address(ctx context.Context, channel, guild string) (channels.Address, error) {
	addr := channels.Address{Bot: a.bot, Chat: channel}
	if !snowflake.MatchString(channel) {
		return addr, channels.ErrInvalid
	}
	if guild == "" {
		return addr, nil
	}
	var info struct {
		ID     string `json:"id"`
		Type   int    `json:"type"`
		Parent string `json:"parent_id"`
	}
	result, err := a.request(ctx, http.MethodGet, "/channels/"+channel, nil, &info)
	if err != nil || result.Outcome != gateway.Delivered || info.ID != channel {
		return addr, errUnavailable
	}
	if info.Type == 10 || info.Type == 11 || info.Type == 12 {
		if !snowflake.MatchString(info.Parent) {
			return addr, errResponse
		}
		addr.Chat, addr.Topic = info.Parent, channel
	}
	return addr, nil
}

// Configure creates or updates only LINA's own command; other application
// commands remain owned by the person. Boot calls this on explicit connect.
func (a *Adapter) Configure(ctx context.Context) error {
	result, err := a.request(ctx, http.MethodPost, "/applications/"+a.bot.Account+"/commands", map[string]any{"name": "lina", "description": "Talk with the agent bound to this chat", "type": 1, "options": []any{map[string]any{"type": 3, "name": "text", "description": "Your message", "required": true}}}, nil)
	if err == nil && result.Outcome != gateway.Delivered {
		return errUnavailable
	}
	return err
}
