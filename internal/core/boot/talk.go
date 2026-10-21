// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/kit/protocol"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The installed main belongs to one person. Both participating agents must
// still be active before any exchange content is projected to that person.
func (r *conversationRuntime) talkAgents(ctx context.Context, from, to string) bool {
	_, a := r.agents.Get(ctx, from)
	_, b := r.agents.Get(ctx, to)
	return a == nil && b == nil
}
func (r *conversationRuntime) Talk(ctx context.Context, s clientsrv.Session, p protocol.AgentTalkGet) (protocol.AgentTalk, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.AgentTalk{}, err
	}
	if p.AgentID != nil {
		if _, err := r.agents.Get(ctx, *p.AgentID); err != nil {
			return protocol.AgentTalk{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
		}
	}
	rows, err := exchange.NewWithMaterials(r.db, r.materials).Log(ctx)
	if err != nil {
		return protocol.AgentTalk{}, err
	}
	entries := []protocol.AgentTalkLine{}
	for _, row := range rows {
		if !r.talkAgents(ctx, row.From, row.To) || p.AgentID != nil && row.From != *p.AgentID && row.To != *p.AgentID {
			continue
		}
		entries = append(entries, protocol.AgentTalkLine{ExchangeID: row.ID, FromAgent: row.From, ToAgent: row.To, Subject: row.Subject, State: string(row.State), Outcome: string(row.Outcome), NeedsPerson: row.NeedsPerson, UpdatedAt: row.At.Format(time.RFC3339Nano)})
	}
	return protocol.AgentTalk{Entries: &entries}, nil
}
func (r *conversationRuntime) ExpandTalk(ctx context.Context, s clientsrv.Session, p protocol.AgentTalkExpand) (protocol.AgentTalk, error) {
	if err := r.authorize(s, nil); err != nil {
		return protocol.AgentTalk{}, err
	}
	store := exchange.NewWithMaterials(r.db, r.materials)
	row, err := store.Get(ctx, *p.ExchangeID)
	if err != nil || !r.talkAgents(ctx, row.From, row.To) {
		return protocol.AgentTalk{}, &clientsrv.OperationError{Code: protocol.ErrorOutOfScope}
	}
	events, err := store.Events(ctx, row.ID)
	if err != nil {
		return protocol.AgentTalk{}, err
	}
	return talkPage(row.ID, events, p.Cursor)
}

// A page contains one event or a UTF-8-safe piece of a large event. Even the
// worst JSON escaping of 32 KiB leaves ample room below the 1 MiB RPC limit.
func talkPage(id string, events []exchange.Event, cursor *string) (protocol.AgentTalk, error) {
	index, offset := 0, 0
	invalid := func() (protocol.AgentTalk, error) {
		return protocol.AgentTalk{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
	}
	if cursor != nil {
		a, b, ok := strings.Cut(*cursor, ":")
		if !ok {
			return invalid()
		}
		var err error
		index, err = strconv.Atoi(a)
		if err != nil {
			return invalid()
		}
		offset, err = strconv.Atoi(b)
		if err != nil {
			return invalid()
		}
	}
	if index < 0 || index > len(events) || offset < 0 {
		return invalid()
	}
	result := []protocol.AgentTalkEvent{}
	response := protocol.AgentTalk{ExchangeID: &id, Events: &result}
	if index == len(events) {
		if offset != 0 {
			return invalid()
		}
		return response, nil
	}
	event := events[index]
	body := event.Message.Body()
	if offset > len(body) || offset < len(body) && !utf8.RuneStart(body[offset]) {
		return invalid()
	}
	end := min(len(body), offset+32*1024)
	for end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	result = append(result, protocol.AgentTalkEvent{AgentID: event.Agent, At: event.At.Format(time.RFC3339Nano), Kind: event.Kind, Text: body[offset:end]})
	if end == len(body) {
		index++
		offset = 0
	} else {
		offset = end
	}
	if index < len(events) {
		next := strconv.Itoa(index) + ":" + strconv.Itoa(offset)
		response.NextCursor = &next
	}
	return response, nil
}
