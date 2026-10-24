// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

// channelChipText is the one line a channel shows under a reply. A text
// channel reaches the move list with one short reply, because a chip line
// cannot be tapped.
func channelChipText(chip *protocol.BindingChip) string {
	if chip.Name == "" {
		return "Not bound to an item · /move"
	}
	return "Bound to " + chip.Name + " · /move"
}

// moveCommand reads "/move" or "/move <number>".
func moveCommand(text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 || fields[0] != "/move" {
		return "", false
	}
	switch len(fields) {
	case 1:
		return "", true
	case 2:
		return fields[1], true
	}
	return "invalid", true
}

// channelMove lists the other candidates for the message the person replied
// to, or applies the numbered choice.
func (c *channelRuntime) channelMove(ctx context.Context, shared *conversationRuntime, event gateway.Inbound, b channels.Binding, in channelInput, replyTo, arg string) error {
	id := "channel-input:" + strconv.FormatInt(in.id, 10)
	now := time.Now()
	input, err := shared.journal().Get(ctx, id)
	if errors.Is(err, ledger.ErrNotFound) {
		input, err = shared.journal().Admit(ctx, guard.EntryOwner, ledger.Input{ID: id, ConversationID: b.Conversation, PersonID: b.Person, AgentID: b.Agent, Surface: ledger.Surface{Kind: "channel", Platform: event.Platform, ChatID: event.Chat, ReplyTo: replyTo}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: in.native})
	}
	if err != nil {
		return err
	}
	claimed := false
	err = c.r.db.Write(ctx, func(tx *canon.Tx) error {
		res, e := tx.ExecContext(ctx, "INSERT INTO channel_steering(inbox_id) VALUES(?) ON CONFLICT DO NOTHING", in.id)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		claimed = n == 1
		return e
	})
	if err != nil || !claimed {
		return err
	}
	text := c.channelMoveText(ctx, shared, event, replyTo, arg)
	return c.channelControlReply(ctx, shared, b, input, text)
}

func (c *channelRuntime) channelMoveText(ctx context.Context, shared *conversationRuntime, event gateway.Inbound, replyTo, arg string) string {
	target, err := c.moveTarget(ctx, shared, event.Address, replyTo)
	if err != nil || target == "" {
		return "Reply to the message that names the item, then send /move."
	}
	original, err := shared.journal().Get(ctx, target)
	if err != nil {
		return "This message has no binding to move."
	}
	var decision binding.Decision
	var offers []binding.Offer
	var chip binding.Chip
	err = shared.db.Read(ctx, func(tx *canon.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT body FROM conv_event WHERE id=? AND scaffold_kind=?", inputBindingID(target), inputBindingKind).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(raw), &decision); err != nil {
			return err
		}
		choices, err := readInputChoices(ctx, tx, original)
		if err != nil {
			return err
		}
		offers = binding.Offers(decision, currentChoice(choices).Target)
		chip, err = shared.boundChip(ctx, tx, original)
		return err
	})
	if err != nil {
		return "This message has no binding to move."
	}
	if arg == "" {
		lines := []string{"Move this message to:"}
		for i, offer := range offers {
			lines = append(lines, strconv.Itoa(i+1)+". "+offerName(offer))
		}
		if chip.Name != "" {
			lines = append(lines, "Now: "+chip.Name)
		}
		return strings.Join(append(lines, "Send /move <number>."), "\n")
	}
	index, err := strconv.Atoi(arg)
	if err != nil || index < 1 || index > len(offers) {
		return "Send /move <number> from the list."
	}
	move, err := shared.moveBinding(ctx, target, offers[index-1].Target, "channel:"+strconv.FormatInt(inboxNumber(event.NativeID), 10)+":"+arg+":"+event.Address.Key())
	if err != nil {
		return "This binding could not be moved."
	}
	moved, left := 0, 0
	for _, effect := range move.Effects {
		if effect.Moved {
			moved++
		} else {
			left++
		}
	}
	line := "Binding moved."
	if moved > 0 {
		line += " " + strconv.Itoa(moved) + " effect(s) moved."
	}
	if left > 0 {
		line += " " + strconv.Itoa(left) + " left with the item it reached."
	}
	return line
}

func offerName(offer binding.Offer) string {
	switch offer.Target.Class {
	case "new":
		return "new item"
	case "none":
		return "no item"
	}
	return offer.Name
}

// moveTarget resolves the message the person replied to, then the input it
// belongs to.
func (c *channelRuntime) moveTarget(ctx context.Context, shared *conversationRuntime, address channels.Address, replyTo string) (string, error) {
	id, err := c.channelReplyEvent(ctx, address, replyTo)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", nil
	}
	event, err := shared.journal().Get(ctx, id)
	if err != nil {
		return "", err
	}
	if event.SpeakerKind == guard.Person {
		return event.ID, nil
	}
	return event.ParentID, nil
}

func inboxNumber(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimPrefix(id, "channel-input:"), 10, 64)
	return n
}

// channelControlReply writes one agent line for a channel command and publishes
// it to the conversation the command belongs to.
func (c *channelRuntime) channelControlReply(ctx context.Context, shared *conversationRuntime, b channels.Binding, input ledger.Event, text string) error {
	now := time.Now()
	response, err := shared.journal().Admit(ctx, guard.EntryAgent, ledger.Input{ID: "channel-control:" + input.ID, ParentID: input.ID, ConversationID: b.Conversation, AgentID: b.Agent, Surface: input.Surface, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	if err != nil {
		return err
	}
	body := response.Body.Text()
	agent := protocol.ConversationSpeaker(response.SpeakerKind)
	return shared.publish(ctx, protocol.KindItemDone, response.ID, protocol.ItemDone{Text: &body, Speaker: &agent, AgentID: &response.AgentID})
}
