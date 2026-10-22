// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/gateway/discord"
	"github.com/thisisjun786/lina/internal/core/gateway/telegram"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

type channelInput struct {
	id                          int64
	native, agent, conversation string
	body                        []byte
}

// The inbox is authoritative. Scanning recovers the gap between admission and
// lane submission; turn.Run's durable claim prevents re-executing a started turn.
func (c *channelRuntime) runChannelWork(ctx context.Context, bot channels.Bot, adapter channelAdapter) {
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		for ctx.Err() == nil {
			if c.r.route != nil {
				_ = c.channelDeliveries(ctx, bot, adapter)
			}
			if channelPause(ctx, 100*time.Millisecond) != nil {
				return
			}
		}
	}()
	defer func() { <-delivered }()
	scheduled := map[int64]bool{}
	for ctx.Err() == nil {
		if c.r.route != nil {
			_ = c.channelWork(ctx, bot, scheduled)
		}
		if channelPause(ctx, 100*time.Millisecond) != nil {
			return
		}
	}
}
func (c *channelRuntime) channelInputs(ctx context.Context, bot channels.Bot) ([]channelInput, error) {
	var inputs []channelInput
	err := c.r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT i.id,r.native_id,r.agent,i.conversation,i.body FROM channel_receipts r JOIN inbox i ON i.id=r.inbox_id WHERE r.bot=? AND i.source=? ORDER BY i.id`, bot.Key(), bot.Key())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var in channelInput
			if err := rows.Scan(&in.id, &in.native, &in.agent, &in.conversation, &in.body); err != nil {
				return err
			}
			inputs = append(inputs, in)
		}
		return rows.Err()
	})
	return inputs, err
}
func (c *channelRuntime) channelBinding(ctx context.Context, in channelInput) (gateway.Inbound, channels.Binding, error) {
	var event gateway.Inbound
	if json.Unmarshal(in.body, &event) != nil {
		return event, channels.Binding{}, channels.ErrInvalid
	}
	var binding channels.Binding
	err := c.r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		binding, err = channels.AdmitScope(ctx, tx, event.Address, event.Sender, event.Direct, event.Addressed)
		return err
	})
	if err == nil && (binding.Agent != in.agent || binding.Conversation != in.conversation || binding.Unreachable) {
		err = channels.ErrDenied
	}
	return event, binding, err
}
func (c *channelRuntime) channelWork(ctx context.Context, bot channels.Bot, scheduled map[int64]bool) error {
	inputs, err := c.channelInputs(ctx, bot)
	if err != nil {
		return err
	}
	for _, in := range inputs {
		var claimed int
		if err = c.r.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM turn_runs WHERE inbox_id=?)+(SELECT count(*) FROM channel_steering WHERE inbox_id=?)", in.id, in.id).Scan(&claimed)
		}); err != nil {
			return err
		}
		if claimed > 0 {
			delete(scheduled, in.id)
			continue
		}
		if scheduled[in.id] {
			continue
		}
		event, binding, err := c.channelBinding(ctx, in)
		if err != nil {
			continue
		}
		shared, err := c.r.router.channelView(ctx, binding)
		if err != nil {
			return err
		}
		if err = c.admitChannelInput(ctx, shared, event, binding, in); err != nil {
			return err
		}
		scheduled[in.id] = true
	}
	return nil
}
func (c *channelRuntime) admitChannelInput(ctx context.Context, shared *conversationRuntime, event gateway.Inbound, binding channels.Binding, in channelInput) error {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(event.Body, &body) != nil {
		return channels.ErrInvalid
	}
	id := "channel-input:" + strconv.FormatInt(in.id, 10)
	input, err := shared.journal().Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		now := time.Now()
		input, err = shared.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: id, ConversationID: binding.Conversation, PersonID: binding.Person, AgentID: binding.Agent, Surface: ledger.Surface{Kind: "channel", Platform: event.Platform, ChatID: event.Chat}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: in.native, Body: body.Text})
	}
	if err != nil {
		return err
	}
	text := input.Body.Text()
	speaker := protocol.ConversationSpeakerPerson
	if err = shared.publish(ctx, protocol.KindItemDone, id, protocol.ItemDone{Text: &text, Speaker: &speaker, PersonID: &binding.Person, AgentID: &binding.Agent}); err != nil {
		return err
	}
	runner := newConversationView(c.r, binding.Agent, binding.Conversation)
	runner.channelBinding = &binding
	runner.coordinator = shared
	return shared.submitInput(input, in.id, true, runner)
}
func (c *channelRuntime) channelDeliveries(ctx context.Context, bot channels.Bot, adapter channelAdapter) error {
	inputs, err := c.channelInputs(ctx, bot)
	if err != nil {
		return err
	}
	for _, in := range inputs {
		event, binding, err := c.channelBinding(ctx, in)
		if err != nil {
			continue
		}
		view := newConversationView(c.r, binding.Agent, binding.Conversation)
		view.channelBinding = &binding
		id := "channel-input:" + strconv.FormatInt(in.id, 10)
		if err = c.channelRunResults(ctx, view, id); err != nil {
			return err
		}
		if err = c.channelReplies(ctx, view, event, in, id, adapter); err != nil {
			return err
		}
	}
	return nil
}

// Before this increment channel inputs were only retained in inbox. Move those
// still-unclaimed private receipts to the owner's main conversation, preserving
// native IDs and every row. Shared chats retain their separate conversation.
func (c *channelRuntime) normalizePrivate(ctx context.Context) error {
	return c.r.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_bindings SET conversation='main:'||(SELECT agent_id FROM agent_bindings WHERE channel=channel_bindings.address) WHERE topic='' AND chat=(SELECT private_chat FROM channel_connections WHERE bot=channel_bindings.bot)`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE inbox SET conversation=(SELECT b.conversation FROM channel_receipts r JOIN channel_bindings b ON b.address=r.address WHERE r.inbox_id=inbox.id) WHERE id IN (SELECT r.inbox_id FROM channel_receipts r JOIN channel_bindings b ON b.address=r.address WHERE b.conversation LIKE 'main:%' AND r.inbox_id IS NOT NULL) AND NOT EXISTS(SELECT 1 FROM turn_runs WHERE inbox_id=inbox.id) AND NOT EXISTS(SELECT 1 FROM channel_steering WHERE inbox_id=inbox.id)`)
		return err
	})
}
func (c *channelRuntime) channelReplies(ctx context.Context, view *conversationRuntime, event gateway.Inbound, in channelInput, parent string, adapter channelAdapter) error {
	var ids []string
	if err := c.r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM conv_event WHERE parent_id=? AND speaker_kind='agent' AND scaffold_kind IS NULL ORDER BY rowid", parent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range ids {
		item, err := view.journal().Get(ctx, id)
		if err != nil {
			return err
		}
		shape := telegram.Shape
		if event.Platform == "discord" {
			shape = discord.Shape
		}
		summary, link := "The full reply is available in LINA.", "lina://conversation/"+view.conversation
		if event.Platform == "discord" {
			summary, link = "", ""
		}
		parts, err := shape(item.Body.Text(), summary, link)
		if err != nil {
			return err
		}
		if err = c.store.Enqueue(ctx, channels.Message{ID: "channel-reply:" + id, Address: event.Address, Agent: in.agent, LedgerRef: id, ReplyTo: in.native, Parts: parts}); err != nil {
			return err
		}
	}
	l, err := c.r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "channel delivery"})
	if err != nil {
		return err
	}
	defer l.Release()
	queue := gateway.NewQueue(c.r.db, adapter)
	for {
		sent, err := queue.SendNext(ctx, l, event.Address, time.Now())
		if err != nil {
			return err
		}
		if !sent {
			return nil
		}
	}
}
func (r *conversationRuntime) validateChannel(ctx context.Context) error {
	if r.channelBinding == nil {
		return nil
	}
	b, err := channels.New(r.db).Binding(ctx, r.channelBinding.Address)
	if err != nil {
		return err
	}
	if b != *r.channelBinding || b.Unreachable {
		return channels.ErrDenied
	}
	return nil
}

// Delivered notices establish the turn boundary. Project each landed artifact
// once into the agent's channel ledger, then let the same durable queue send it
// as a new message. An unknown send never repeats on restart.
func (c *channelRuntime) channelRunResults(ctx context.Context, view *conversationRuntime, parent string) error {
	deliveries, err := c.r.background.notices.Delivered(ctx, view.conversation)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		for _, n := range d.Notices {
			record, err := c.r.background.service.Get(ctx, n.Run)
			if err != nil {
				return err
			}
			if record.Origin.Turn != "turn:"+parent || record.Origin.Agent != view.agent || record.Origin.Person != view.person {
				continue
			}
			id := "channel-result:" + n.ID
			if _, err = view.journal().Get(ctx, id); err == nil {
				continue
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			text := n.Summary
			if n.Result != "" {
				body, next, err := c.r.background.service.ResultPage(ctx, view.person, n.Run, 0)
				if err != nil {
					return err
				}
				for next != 0 {
					page, more, e := c.r.background.service.ResultPage(ctx, view.person, n.Run, next)
					if e != nil {
						return e
					}
					body += page
					next = more
					if len(body) > 8<<20 {
						return channels.ErrInvalid
					}
				}
				text += "\n\n" + body
			}
			b := view.channelBinding
			_, err = view.journal().Admit(ctx, guard.EntryAgent, ledger.Input{ID: id, ParentID: parent, ConversationID: view.conversation, AgentID: view.agent, Surface: ledger.Surface{Kind: "channel", Platform: b.Platform, ChatID: b.Chat}, OccurredAt: n.Landed, RecordedAt: n.Landed, TimeQuality: "recorded", Locator: n.ID, Body: text})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
