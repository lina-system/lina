// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

type resultPageClient struct {
	shellClient
	requests []protocol.ResultGet
}

func (c *resultPageClient) Result(_ context.Context, request protocol.ResultGet) (string, error) {
	c.requests = append(c.requests, request)
	return fmt.Sprintf("page-%d", len(c.requests)), nil
}

func TestAuthoredResultPagerReadsAllPages(t *testing.T) {
	m := conversationShell()
	m.conversation.id = "main:agent"
	first := strings.Repeat("Full text.\n", 7000)
	last := "RESULT-END"
	next := json.Number(fmt.Sprint(len(first)))
	card := protocol.Result{ResultID: "authored", Revision: "3", Text: &first, Next: &next,
		Provenance: protocol.ResultProvenance{ConversationID: m.conversation.id}}
	block := state.Block{ID: "card", Kind: protocol.KindResult, Topic: "conversation:" + m.conversation.id, Done: true}
	projectConversationBlock(&block, card)
	m.content.Blocks = []state.Block{block}
	m.content.Draft.Text = "Retained draft"
	m.refreshResultStreams(state.Snapshot{})
	command := m.conversationKey(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if command == nil {
		t.Fatal("Ctrl+O did not request the retained result")
	}
	request := command().(shellManagement).Request.(protocol.ResultGet)
	client := &resultPageClient{}
	session := managementSession{model: m, pending: map[string]protocol.Payload{}}
	if err := session.request(t.Context(), client, request); err != nil {
		t.Fatal(err)
	}
	reply := func(id string, page protocol.Result) {
		t.Helper()
		if handled, err := session.response(t.Context(), client, protocol.Envelope{ReplyTo: &id, Payload: page}); !handled || err != nil {
			t.Fatalf("result response: handled=%t err=%v", handled, err)
		}
	}
	reply("page-1", card)
	if len(client.requests) != 2 || client.requests[1].Offset == nil || *client.requests[1].Offset != next || *client.requests[1].Revision != "3" {
		t.Fatalf("continuation did not pin revision/offset: %+v", client.requests)
	}
	m.conversation.pager.followEnd = true
	card.Text, card.Next = &last, nil
	reply("page-2", card)
	m.refreshOpenCard()
	if !strings.Contains(strings.Join(m.pagerLines(), "\n"), last) {
		t.Fatal("rendered pager still uses the cached first-page preview")
	}
	if m.conversation.pager.offset == 0 {
		t.Fatal("End did not follow the completed result")
	}
	if m.conversation.pager.text != first+last || m.content.Draft.Text != "Retained draft" {
		t.Fatal("pager lost full text or changed the draft")
	}
	saved := m.conversation.pager
	for _, removed := range []bool{false, true} {
		m.conversation.pager = saved
		if removed {
			m.content.Blocks = nil
		} else {
			m.content.Blocks[0].ResultRevision = "4"
		}
		m.refreshOpenCard()
		if m.conversation.pager.open {
			t.Fatal("pager retained content from an obsolete or removed card")
		}
	}
}

func TestResultPageResponseDoesNotReopenClosedPager(t *testing.T) {
	m := conversationShell()
	revision := json.Number("1")
	request := protocol.ResultGet{ResultID: "result", Revision: &revision}
	m.openPager("Preview")
	m.conversation.pager.result = &resultPager{request: request}
	client := &resultPageClient{}
	session := managementSession{model: m, pending: map[string]protocol.Payload{}}
	if err := session.request(t.Context(), client, request); err != nil {
		t.Fatal(err)
	}
	m.conversation.pager = textPager{}
	id := "page-1"
	if handled, err := session.response(t.Context(), client, protocol.Envelope{ReplyTo: &id, Payload: protocol.Result{}}); !handled || err != nil {
		t.Fatalf("late page: %t %v", handled, err)
	}
	if m.conversation.pager.open || len(client.requests) != 1 {
		t.Fatal("closed pager reopened or fetched another page")
	}
}
