// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) authorize(session clientsrv.Session, conversation *string) error {
	if session.PersonID != r.person {
		return &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	if conversation != nil && *conversation != r.conversation {
		return &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	return nil
}

func (r *conversationRuntime) History(ctx context.Context, s clientsrv.Session, p protocol.HistoryGet) (protocol.History, error) {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return protocol.History{}, err
	}
	if p.AgentID != nil && *p.AgentID != r.agent {
		return protocol.History{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	// Subscriptions own replay and live ordering. History discovers the concrete
	// topic; it does not race a second independently captured history window.
	return protocol.History{ConversationID: &r.conversation, AgentID: &r.agent}, nil
}

func (r *conversationRuntime) Input(ctx context.Context, s clientsrv.Session, p protocol.Input) (protocol.ItemDone, error) {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return protocol.ItemDone{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	masked, err := secrets.Mask(*p.Text)
	if err != nil {
		return protocol.ItemDone{}, err
	}
	digest := sha256.Sum256([]byte(r.person + "\x00" + r.conversation + "\x00" + *p.InputID))
	eventID := "input:" + hex.EncodeToString(digest[:])
	admission := &conversationAdmission{db: r.db, person: r.person, agent: r.agent, input: inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "client", NativeID: *p.InputID}, Body: []byte(masked.Text())}}
	language, err := r.inputLanguage(ctx, *p.Text)
	if err != nil {
		return protocol.ItemDone{}, err
	}
	now := time.Now()
	event, err := ledger.New(admission, r.materials).Admit(ctx, guard.EntryOwner, ledger.Input{ID: eventID, ConversationID: r.conversation, PersonID: r.person, AgentID: r.agent, Surface: ledger.Surface{Kind: "tui", Language: string(language)}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: *p.InputID, Body: *p.Text})
	receipt := admission.receipt
	if errors.Is(err, errAdmittedInput) {
		event, err = r.journal().Get(ctx, eventID)
	}
	if err != nil {
		return protocol.ItemDone{}, err
	}
	text := event.Body.Text()
	speaker := protocol.ConversationSpeakerPerson
	answer := protocol.ItemDone{Text: &text, EchoKey: p.InputID, Speaker: &speaker, AgentID: &r.agent, PersonID: &r.person}
	if !receipt.Inserted {
		return answer, nil
	}
	if err = r.publish(ctx, protocol.KindItemDone, eventID, answer); err != nil {
		return protocol.ItemDone{}, err
	}
	if r.intakeReplay != nil {
		return answer, r.intakeReplay(ctx, event)
	}
	err = r.submitInput(event, receipt.ID, p.Mode == nil || *p.Mode == protocol.InputModeSteer, r)
	return answer, err
}

func (r *conversationRuntime) Interrupt(ctx context.Context, s clientsrv.Session, p protocol.Interrupt) error {
	if err := r.authorize(s, p.ConversationID); err != nil {
		return err
	}
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	if err := r.phaseLocked(ctx, protocol.TurnPhaseStopping, ""); err != nil {
		return err
	}
	r.lanes.Stop(r.conversation, turn.Interrupt{Issuer: turn.Person})
	return nil
}

// topicAuthorized runs at subscription admission and before each delivery.
func (r *conversationRuntime) topicAuthorized(ctx context.Context, topic string) bool {
	for _, prefix := range []string{"conversation:main:", "results:main:"} {
		if len(topic) > len(prefix) && topic[:len(prefix)] == prefix {
			_, err := r.agents.Get(ctx, topic[len(prefix):])
			return err == nil
		}
	}
	return topic == "progress" && r.ownerViews != nil || topic == "running" && r.background != nil || topic == "core" || topic == "waiting" && r.decisions != nil
}
