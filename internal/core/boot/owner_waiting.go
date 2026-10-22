// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

// Each live request is read under its session lock. No adapter lock is acquired
// from canon's writer: worker callbacks themselves commit conversation evidence.
type workerWaitingSource struct {
	r     *conversationRuntime
	names map[string]string
}

func (s workerWaitingSource) Snapshot(_ context.Context, person string) ([]waiting.LiveItem, error) {
	items := []waiting.LiveItem{}
	for _, session := range s.r.workers.entries() {
		name, visible := s.names[session.origin.agent]
		if session.origin.person != person || !visible {
			continue
		}
		session.mu.Lock()
		for id, q := range session.questions {
			card := q.card.Card
			revision, err := strconv.ParseUint(card.Revision, 10, 64)
			if err != nil {
				session.mu.Unlock()
				return nil, err
			}
			items = append(items, waiting.LiveItem{ID: id, Item: waiting.Item{Person: person, Agent: session.origin.agent, Revision: revision, Reason: waiting.MissingInformation, Blocking: true, Finished: card.Outcome != nil, View: view.Waiting{Kind: view.Reply, Sentence: card.Summary, AgentName: name, FreeText: true, Effort: view.FiveMinutes}}})
		}
		session.mu.Unlock()
	}
	return items, nil
}
