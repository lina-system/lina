// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/sanitize"
	"github.com/thisisjun786/lina/kit/protocol"
)

type resultPager struct {
	request   protocol.ResultGet
	requestID string
	text      strings.Builder
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
	m.openSurface(card.ResultID, card.ResultRevision, card.SurfaceControls)
	if !card.ResultPaged || !m.mutationsAllowed() {
		return nil
	}
	revision := json.Number(card.ResultRevision)
	conversation := m.conversation.id
	request := protocol.ResultGet{ResultID: card.ResultID, Revision: &revision, ConversationID: &conversation}
	m.conversation.pager.result = &resultPager{request: request}
	generation := m.generation
	return func() tea.Msg { return shellManagement{Generation: generation, Request: request} }
}

func (m *shellModel) resultPageResponse(request protocol.ResultGet, page protocol.Result) (*protocol.ResultGet, error) {
	p := &m.conversation.pager
	if p.result == nil || request.Revision == nil || page.ResultID != request.ResultID || page.Revision != *request.Revision ||
		request.ConversationID == nil || page.Provenance.ConversationID != *request.ConversationID || page.Text == nil {
		return nil, errStream
	}
	var offset int64
	var err error
	if request.Offset != nil {
		offset, err = request.Offset.Int64()
	}
	if err != nil || offset != int64(p.result.text.Len()) {
		return nil, errStream
	}
	p.result.text.WriteString(*page.Text)
	if page.Next != nil {
		next, err := page.Next.Int64()
		if err != nil || next <= offset || next != int64(p.result.text.Len()) {
			return nil, errStream
		}
		request.Offset = page.Next
		return &request, nil
	}
	p.text = sanitize.Display(p.result.text.String())
	if page.Type == protocol.ResultTypeSurface && page.SurfaceControls != nil {
		m.openSurface(page.ResultID, string(page.Revision), page.SurfaceControls)
	}
	if p.followEnd {
		p.offset = len(m.pagerLines())
	}
	return nil, nil
}
