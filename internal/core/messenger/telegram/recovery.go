// SPDX-License-Identifier: Apache-2.0
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/gotd/td/tg"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

var ErrUpdateGap = errors.New("telegram_update_gap")

type channelPosition struct {
	Pts        int
	AccessHash int64
}
type updatePosition struct {
	Version  int
	State    tg.UpdatesState
	Channels map[int64]channelPosition
}

// Recover reconciles updates missed while offline before sync marks a source
// current. A checkpoint advances only after every event in its response commits.
func (a *Adapter) Recover(ctx context.Context, source messenger.SourceID, deliver func(messenger.Event) error) error {
	if source.Platform != Platform || source.Account != a.cfg.Account || deliver == nil {
		return ErrInvalid
	}
	a.recoveryMu.Lock()
	defer a.recoveryMu.Unlock()
	api, err := a.api(ctx)
	if err != nil {
		return err
	}
	key := credentialKey(a.cfg.Person, "updates:"+a.cfg.Account)
	position := updatePosition{Version: 1, Channels: map[int64]channelPosition{}}
	blob, err := a.cfg.Vault.GetBlob(key)
	fresh := errors.Is(err, secrets.ErrNotFound)
	if err != nil && !fresh {
		return err
	}
	if !fresh {
		if err := json.Unmarshal(blob.Bytes(), &position); err != nil || position.Version != 1 || position.Channels == nil {
			return ErrUpdateGap
		}
	} else {
		var state *tg.UpdatesState
		if err := a.retry(ctx, func() error { var err error; state, err = api.UpdatesGetState(ctx); return err }); err != nil {
			return err
		}
		position.State = *state
	}
	// Dialog pts establishes the baseline before a channel's first history fill.
	a.mu.Lock()
	for id, pts := range a.channelPts {
		entity := a.channels[id]
		if entity == nil || pts <= 0 {
			continue
		}
		previous, exists := position.Channels[id]
		if !exists {
			previous.Pts = pts
		}
		previous.AccessHash = entity.AccessHash
		position.Channels[id] = previous
	}
	a.mu.Unlock()
	save := func() error {
		data, err := json.Marshal(position)
		if err != nil {
			return err
		}
		return (vaultSession{store: a.cfg.Vault, key: key}).StoreSession(ctx, data)
	}
	if fresh {
		return save()
	}
	if err := a.recoverCommon(ctx, api, &position, deliver, save); err != nil {
		return err
	}
	ids := make([]int64, 0, len(position.Channels))
	for id := range position.Channels {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if err := a.recoverChannel(ctx, api, &position, id, deliver, save); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) recoverCommon(ctx context.Context, api *tg.Client, position *updatePosition, deliver func(messenger.Event) error, save func() error) error {
	for range 100 {
		var response tg.UpdatesDifferenceClass
		before := position.State
		if err := a.retry(ctx, func() error {
			var err error
			response, err = api.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: before.Pts, Qts: before.Qts, Date: before.Date})
			return err
		}); err != nil {
			return err
		}
		var messages []tg.MessageClass
		var updates []tg.UpdateClass
		var users []tg.UserClass
		var chats []tg.ChatClass
		next, done := before, false
		switch v := response.(type) {
		case *tg.UpdatesDifference:
			messages, updates, users, chats, next, done = v.NewMessages, v.OtherUpdates, v.Users, v.Chats, v.State, true
		case *tg.UpdatesDifferenceSlice:
			messages, updates, users, chats, next = v.NewMessages, v.OtherUpdates, v.Users, v.Chats, v.IntermediateState
		case *tg.UpdatesDifferenceEmpty:
			next.Date, next.Seq, done = v.Date, v.Seq, true
		default:
			return ErrUpdateGap
		}
		if !done && next.Pts <= before.Pts && next.Qts <= before.Qts && next.Date <= before.Date && next.Seq <= before.Seq {
			return ErrUpdateGap
		}
		if err := a.recoveredEvents(messages, updates, users, chats, deliver); err != nil {
			return err
		}
		position.State = next
		if err := save(); err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return ErrUpdateGap
}

func (a *Adapter) recoverChannel(ctx context.Context, api *tg.Client, position *updatePosition, id int64, deliver func(messenger.Event) error, save func() error) error {
	for range 100 {
		before := position.Channels[id]
		var response tg.UpdatesChannelDifferenceClass
		if err := a.retry(ctx, func() error {
			var err error
			response, err = api.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{Channel: &tg.InputChannel{ChannelID: id, AccessHash: before.AccessHash}, Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: before.Pts, Limit: 100})
			return err
		}); err != nil {
			return err
		}
		var messages []tg.MessageClass
		var updates []tg.UpdateClass
		var users []tg.UserClass
		var chats []tg.ChatClass
		pts, done := before.Pts, false
		switch v := response.(type) {
		case *tg.UpdatesChannelDifference:
			messages, updates, users, chats, pts, done = v.NewMessages, v.OtherUpdates, v.Users, v.Chats, v.Pts, v.Final
		case *tg.UpdatesChannelDifferenceEmpty:
			pts, done = v.Pts, true
		default:
			return ErrUpdateGap
		}
		if !done && pts <= before.Pts {
			return ErrUpdateGap
		}
		if err := a.recoveredEvents(messages, updates, users, chats, deliver); err != nil {
			return err
		}
		before.Pts = pts
		position.Channels[id] = before
		if err := save(); err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return ErrUpdateGap
}

func (a *Adapter) recoveredEvents(messages []tg.MessageClass, updates []tg.UpdateClass, users []tg.UserClass, chats []tg.ChatClass, deliver func(messenger.Event) error) error {
	a.mu.Lock()
	a.rememberLocked(users, chats)
	a.mu.Unlock()
	message := func(raw tg.MessageClass) error {
		value, ok := raw.(*tg.Message)
		if !ok || value == nil {
			return nil
		}
		a.mu.Lock()
		converted, ok := a.messageLocked(value)
		chat, _, _, known := a.chatLocked(value.PeerID)
		a.mu.Unlock()
		if !ok || !known {
			return ErrUpdateGap
		}
		return deliver(messenger.Event{Chat: chat, Message: &converted})
	}
	deletion := func(chat string, ids []int) error {
		for _, id := range ids {
			event := messenger.Event{Chat: chat, Deletion: &messenger.Deletion{Message: strconv.Itoa(id), At: a.now()}}
			if chat == "" {
				event.ChatPrefixes = []string{"user:", "chat:"}
			}
			if err := deliver(event); err != nil {
				return err
			}
		}
		return nil
	}
	for _, value := range messages {
		if err := message(value); err != nil {
			return err
		}
	}
	for _, value := range updates {
		var err error
		switch update := value.(type) {
		case *tg.UpdateEditMessage:
			err = message(update.Message)
		case *tg.UpdateEditChannelMessage:
			err = message(update.Message)
		case *tg.UpdateDeleteMessages:
			err = deletion("", update.Messages)
		case *tg.UpdateDeleteChannelMessages:
			err = deletion("channel:"+strconv.FormatInt(update.ChannelID, 10), update.Messages)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
