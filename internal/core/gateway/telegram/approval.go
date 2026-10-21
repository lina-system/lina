// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"errors"
	"strconv"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/internal/core/authority"
)

// ownerLookup reads the current pairing and binding for numeric platform IDs.
// It must reject revoked bindings, non-owners and bindings without tools. Its
// person and agent come from Core's channel store, never from callback data.
type ownerLookup func(ctx context.Context, user, chat, thread int64) (person, agent string, err error)

// presentationLookup matches an approval to the platform message whose full
// canonical request was sent by PresentApproval. Callback data is untrusted.
type presentationLookup func(ctx context.Context, id string, chat, thread, message int64) (bool, error)

type approvalService interface {
	Approval(context.Context, string) (authority.Approval, error)
	Answer(context.Context, string, authority.Decider, authority.Decision) error
}

type buttonDecision struct {
	accepted, remove bool
	note             string
}

func decideButton(ctx context.Context, epoch uint64, botID string, q gotgbot.CallbackQuery, owner ownerLookup, approvals approvalService, presented presentationLookup) buttonDecision {
	refused := buttonDecision{note: "This button is unavailable."}
	id, choice, ok := parseButton(q.Data)
	message, accessible := q.Message.(gotgbot.Message)
	if !ok || !accessible || q.From.Id <= 0 || q.From.IsBot || message.Chat.Id == 0 || message.MessageId <= 0 || owner == nil || approvals == nil || presented == nil || epoch == 0 {
		return refused
	}
	if message.From == nil || !message.From.IsBot || strconv.FormatInt(message.From.Id, 10) != botID {
		return refused
	}
	person, agent, err := owner(ctx, q.From.Id, message.Chat.Id, message.MessageThreadId)
	if err != nil || person == "" || agent == "" {
		return refused
	}
	matched, err := presented(ctx, id, message.Chat.Id, message.MessageThreadId, message.MessageId)
	if err != nil || !matched {
		return refused
	}
	a, err := approvals.Approval(ctx, id)
	if err != nil || a.Person != person || a.Agent != agent {
		return refused
	}
	if a.State != authority.Pending || a.Epoch != epoch {
		refused.remove = true
		return refused
	}
	err = approvals.Answer(ctx, id, authority.Decider{
		Person: person, Epoch: epoch, Surface: authority.Channel, Authenticated: true,
		ChatID: strconv.FormatInt(message.Chat.Id, 10), MessageID: strconv.FormatInt(message.MessageId, 10),
	}, authority.Decision{Choice: choice})
	if err != nil {
		refused.remove = errors.Is(err, authority.ErrStale)
		return refused
	}
	note := "Allowed once."
	if choice == authority.Deny {
		note = "Denied."
	}
	return buttonDecision{accepted: true, remove: true, note: note}
}
