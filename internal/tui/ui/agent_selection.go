// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func switchConversation(ctx context.Context, c *clientapi.Client, m *shellModel, agent string) error {
	if m.conversation.id != "" {
		off := false
		for _, topic := range []string{"conversation:" + m.conversation.id, "results:" + m.conversation.id} {
			if _, err := c.Subscribe(ctx, protocol.Subscribe{Topic: &topic, Active: &off}); err != nil {
				return err
			}
		}
	}
	m.reduceInput(state.FocusConversation{})
	m.conversation.id = ""
	m.conversation.scroll = 0
	m.conversation.undo = nil
	if agent == "" {
		return nil
	}
	// Set the expected identity now, so late events from the old subscriptions
	// cannot be projected while discovery of the new lane is in flight.
	m.conversation.id = "main:" + agent
	_, err := c.History(ctx, protocol.HistoryGet{AgentID: &agent})
	return err
}
