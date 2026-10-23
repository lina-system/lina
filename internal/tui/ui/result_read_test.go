// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type resultPeer struct {
	shellClient
	requests []protocol.ResultGet
}

func (p *resultPeer) Result(_ context.Context, request protocol.ResultGet) (string, error) {
	p.requests = append(p.requests, request)
	return string(rune('a' + len(p.requests))), nil
}

func TestResultPagerReadsAllPagesAndFencesLateReplies(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "results", Version: "1.0"})
	m.content.Draft.Text = "KEEP DRAFT"
	m.content.Blocks = []state.Block{{ID: "card", Kind: protocol.KindResult, ResultID: "opaque", ResultRevision: "3", ResultTruncated: true, Body: "PREVIEW"}}
	cmd := m.openResultCard()
	if cmd == nil {
		t.Fatal("no detail request")
	}
	request := cmd().(shellManagement).Request.(protocol.ResultGet)
	peer := &resultPeer{}
	s := managementSession{model: m, pending: map[string]protocol.Payload{}}
	if err := s.request(t.Context(), peer, request); err != nil {
		t.Fatal(err)
	}
	first, next := "FIRST\n", json.Number("6")
	id := m.conversation.pager.result.requestID
	if _, err := s.response(t.Context(), peer, protocol.Envelope{ReplyTo: &id, Payload: protocol.Result{ResultID: "opaque", Revision: "3", Text: &first, Next: &next}}); err != nil {
		t.Fatal(err)
	}
	if len(peer.requests) != 2 || peer.requests[1].Offset == nil || *peer.requests[1].Revision != "3" {
		t.Fatal("page did not pin revision and offset")
	}
	last := "FINAL\x1b]52;c;secret\a"
	id = m.conversation.pager.result.requestID
	if _, err := s.response(t.Context(), peer, protocol.Envelope{ReplyTo: &id, Payload: protocol.Result{ResultID: "opaque", Revision: "3", Text: &last}}); err != nil {
		t.Fatal(err)
	}
	m.refreshOpenCard()
	if m.conversation.pager.text != "FIRST\nFINAL" || strings.Contains(strings.Join(m.pagerLines(), "\n"), "PREVIEW") || m.content.Draft.Text != "KEEP DRAFT" {
		t.Fatal("pager used preview, unsafe text or changed draft", m.conversation.pager.text)
	}
	// A revision update must invalidate both retained detail and in-flight pages.
	m.content.Blocks[0].ResultRevision = "4"
	m.refreshOpenCard()
	if m.conversation.pager.result != nil || strings.Contains(m.conversation.pager.text, "FINAL") {
		t.Fatal("old revision retained")
	}
	for _, failure := range []bool{false, true} {
		m.openResultCard()
		if err := s.request(t.Context(), peer, request); err != nil {
			t.Fatal(err)
		}
		id = m.conversation.pager.result.requestID
		m.openPager("another pager")
		e := protocol.Envelope{ReplyTo: &id, Payload: protocol.Result{ResultID: "opaque", Revision: "3", Text: &last}}
		if failure {
			e.Error = &protocol.Error{Code: protocol.ErrorUnavailable}
		}
		if _, err := s.response(t.Context(), peer, e); err != nil {
			t.Fatal(err)
		}
		if m.conversation.pager.text != "another pager" {
			t.Fatal("late reply replaced current pager")
		}
	}
	m.openResultCard()
	m.content.Blocks = nil
	m.refreshOpenCard()
	if m.conversation.pager.open {
		t.Fatal("removed card retained full detail")
	}
}
