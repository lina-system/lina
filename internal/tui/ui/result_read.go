// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"context"
	"encoding/json"
	"strconv"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

type resultPager struct {
	id, requestID, raw string
	revision           json.Number
}

func (m *shellModel) openResultCard() tea.Cmd {
	card, ok := m.selectedCard()
	if ok && card.RunID != "" {
		return m.runningRequest(card.RunID, "open", nil)
	}
	m.openPager(m.cardText())
	if !ok {
		return nil
	}
	m.conversation.pager.cardID = card.ID
	if !card.ResultTruncated || !m.hasCapability("results") {
		return nil
	}
	detail := &resultPager{id: card.ResultID, revision: card.ResultRevision}
	m.conversation.pager.result = detail
	generation := m.generation
	return func() tea.Msg {
		return shellManagement{Generation: generation, Result: detail, Request: protocol.ResultGet{ResultID: detail.id, Revision: &detail.revision}}
	}
}

func (s *managementSession) resultResponse(ctx context.Context, c shellClient, request protocol.ResultGet, value protocol.Result) error {
	p := &s.model.conversation.pager
	detail := p.result
	if value.ResultID != request.ResultID || value.Revision != detail.revision || value.Text == nil {
		return errStream
	}
	var offset int64
	if request.Offset != nil {
		offset, _ = request.Offset.Int64()
	}
	if offset != int64(len(detail.raw)) {
		return errStream
	}
	detail.raw += *value.Text
	p.text = sanitize.Display(detail.raw)
	if p.followEnd {
		p.offset = len(s.model.pagerLines())
	}
	if value.Next != nil {
		next, err := value.Next.Int64()
		if err != nil || next <= offset || next != int64(len(detail.raw)) {
			return errStream
		}
		number := json.Number(strconv.FormatInt(next, 10))
		return s.request(ctx, c, protocol.ResultGet{ResultID: detail.id, Revision: &detail.revision, Offset: &number})
	}
	return nil
}
