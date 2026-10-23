// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/work"
)

// Core captures the originating authority before prepare_work can admit a task.
// A plain origin is explicit too: private channels can share main conversations.
type completionOrigin struct {
	Person, Agent, Conversation string
	Binding                     *channels.Binding
	Address                     channels.Address
}

func (r *conversationRuntime) captureCompletionOrigin(ctx context.Context, id, agent, conversation string) error {
	if err := r.validateChannel(ctx); err != nil {
		return err
	}
	origin := completionOrigin{Person: r.person, Agent: agent, Conversation: conversation, Binding: r.channelBinding, Address: r.channelAddress}
	raw, err := json.Marshal(origin)
	if err != nil {
		return err
	}
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		var prior []byte
		err := tx.QueryRowContext(ctx, "SELECT record FROM work_completion_origins WHERE task_id=?", id).Scan(&prior)
		if err == nil {
			var saved completionOrigin
			if err = json.Unmarshal(prior, &saved); err != nil {
				return err
			}
			if !reflect.DeepEqual(saved, origin) {
				return channels.ErrDenied
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", id).Scan(&prior)
		if err == nil {
			var card work.Card
			if err = json.Unmarshal(prior, &card); err != nil {
				return err
			}
			if card.Task.AgentID != agent || card.Task.ConversationID != conversation || card.Task.PersonID != "" && card.Task.PersonID != r.person {
				return channels.ErrDenied
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO work_completion_origins(task_id,record) VALUES(?,?)", id, raw)
		return err
	})
}

func (r *conversationRuntime) completionRunner(ctx context.Context, card work.Card) (*conversationRuntime, error) {
	var origin completionOrigin
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM work_completion_origins WHERE task_id=?", card.Task.ID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &origin)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, channels.ErrDenied
	}
	if err != nil {
		return nil, err
	}

	if origin.Person != r.person || origin.Agent != card.Task.AgentID || origin.Conversation != card.Task.ConversationID {
		return nil, channels.ErrDenied
	}
	// Ownership stays with the dispatch-enabled manager. A delegated result
	// resumes the requester using the authority captured before task admission.
	agent, conversation := origin.Agent, origin.Conversation
	var assignment codingAssignment
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		assignment, err = readCoding(ctx, tx, card.Task.ID)
		return err
	})
	if err == nil {
		if assignment.Owner != card.Task.AgentID || assignment.Conversation != card.Task.ConversationID || assignment.Origin.Person != origin.Person || assignment.Origin.Agent == "" || assignment.Origin.Conversation == "" {
			return nil, channels.ErrDenied
		}
		agent, conversation = assignment.Origin.Agent, assignment.Origin.Conversation
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	shared, err := r.router.channelView(ctx, channels.Binding{Person: r.person, Agent: agent, Conversation: conversation})
	if err != nil {
		return nil, err
	}
	if origin.Binding == nil {
		return shared, nil
	}
	runner := newConversationView(r.router.root, agent, conversation)
	runner.channelBinding = origin.Binding
	runner.channelAddress = origin.Address
	runner.coordinator = shared
	if err = runner.validateChannel(ctx); err != nil {
		return nil, err
	}
	return runner, nil
}
