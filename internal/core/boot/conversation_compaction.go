// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/provider"
)

// Fit the complete prepared request before applying its hard size limit. Canon
// remains lossless: only a published summary or a recoverable tool note changes
// the request projection. A failed generation/publication never drops sources.
func (r *conversationRuntime) compactConversation(ctx context.Context, in assemble.Input, events []ledger.Event, stream func(context.Context, provider.Request) (provider.Stream, error), bind ...func(*conversationCompaction)) (provider.Request, assemble.Report, error) {
	limit := in.MaxTokens
	_, count := r.conversationBudget()
	if limit <= 0 || count == nil {
		return provider.Request{}, assemble.Report{}, compaction.ErrPass
	}
	if len(events) == 0 {
		return provider.Request{}, assemble.Report{}, assemble.ErrHistory
	}
	projected, err := assemble.History(events[:len(events)-1], r.conversation, r.agent)
	if err != nil {
		return provider.Request{}, assemble.Report{}, err
	}
	prefixCount := len(in.History) - len(projected)
	if prefixCount < 0 {
		return provider.Request{}, assemble.Report{}, assemble.ErrHistory
	}
	for i, item := range projected {
		if !bytes.Equal(item, in.History[prefixCount+i]) {
			return provider.Request{}, assemble.Report{}, assemble.ErrHistory
		}
	}
	// modelContext supplies profile projections before the ledger projection.
	// They belong in the transient packet, outside history and compaction.
	in.Profile = append(append([]provider.Item(nil), in.Profile...), in.History[:prefixCount]...)
	in.History = in.History[prefixCount:]
	// Build the fixed prefix without freezing optional-context budget drops.
	// The published root must replace its covered events before size selection.
	in.MaxTokens = int(^uint(0) >> 1)
	request, report := assemble.Assemble(in, count)
	if report.Failure != "" {
		return provider.Request{}, report, assemble.ErrHistory
	}
	fixed := len(request.Items) - len(in.History) - len(report.Items) - len(in.Profile) - 1
	if fixed < 0 {
		return provider.Request{}, report, assemble.ErrHistory
	}
	current := events[len(events)-1]
	view := compaction.View{Request: request, Fixed: request.Items[:fixed], Packet: request.Items[fixed+len(in.History)+1:], Input: compaction.Record{ID: current.ID, Item: in.Current, User: current.SpeakerKind == guard.Person}}
	project := func(ctx context.Context, id string) (compaction.Record, error) {
		event, err := r.journal().Get(ctx, id)
		if err != nil {
			return compaction.Record{}, err
		}
		items, err := assemble.History([]ledger.Event{event}, r.conversation, r.agent)
		if err != nil || len(items) != 1 {
			return compaction.Record{}, assemble.ErrHistory
		}
		record := compaction.Record{ID: id, Item: items[0], User: event.SpeakerKind == guard.Person}
		if event.SpeakerKind == guard.Tool && event.ScaffoldKind == "provider_item" {
			var item struct {
				CallID string `json:"call_id"`
				Output string
			}
			var receipt struct{ Code string }
			if json.Unmarshal(items[0], &item) == nil && json.Unmarshal([]byte(item.Output), &receipt) == nil && receipt.Code != "" {
				record.Note = fmt.Sprintf("Tool call %s returned %s", item.CallID, receipt.Code)
			}
		}
		return record, nil
	}
	// The auxiliary caller preserves only the fixed prefix and the selected
	// segment, never this turn's input or transient ContextPacket.
	auxiliaryRequest := request
	auxiliaryRequest.Credential = r.route.Request(nil).Credential
	auxiliaryRequest.Items = request.Items[:fixed] // Profiles must not become summary source text.
	auxiliary := compaction.Auxiliary{Request: auxiliaryRequest, RetainPrefix: true, Stream: stream}
	store := compaction.New(r.db)
	frontier, err := store.Frontier(ctx, r.conversation)
	if err != nil {
		return provider.Request{}, report, err
	}
	covered := map[string]bool{}
	if frontier.Root != "" {
		node, err := store.Node(ctx, frontier.Root)
		if errors.Is(err, compaction.ErrUnavailable) {
			node, err = store.Rebuild(ctx, r.conversation, func(ctx context.Context, segment compaction.Segment) (compaction.Output, error) {
				var sources []compaction.Record
				for _, child := range segment.Children {
					var text compaction.Text
					if err := json.Unmarshal([]byte(child.Output.Text), &text); err != nil {
						return compaction.Output{}, err
					}
					sources = append(sources, compaction.Record{ID: "node:" + child.ID, Item: compaction.SummaryItem(text, false)})
				}
				for _, id := range segment.Events {
					item, err := project(ctx, id)
					if err != nil {
						return compaction.Output{}, err
					}
					sources = append(sources, item)
				}
				text, err := auxiliary.Summarize(ctx, compaction.PassSegment{Records: sources})
				if err != nil {
					return compaction.Output{}, err
				}
				raw, err := json.Marshal(text)
				return compaction.Output{Model: string(in.Model), PromptVersion: compaction.PromptVersion, BuiltAt: time.Now(), Text: string(raw), Dropped: text.Omitted}, err
			})
		}
		if err != nil {
			return provider.Request{}, report, err
		}
		var text compaction.Text
		if err := json.Unmarshal([]byte(node.Output.Text), &text); err != nil {
			return provider.Request{}, report, err
		}
		view.RootID, view.Root = node.ID, compaction.SummaryItem(text, false)
		for offset := 0; ; {
			ids, next, err := store.ExpandEvents(ctx, r.agent, node.ID, offset, 1000)
			if err != nil {
				return provider.Request{}, report, err
			}
			for _, id := range ids {
				covered[id] = true
			}
			if next == 0 {
				break
			}
			offset = next
		}
	}
	for _, event := range events[:len(events)-1] {
		if covered[event.ID] {
			continue
		}
		items, err := assemble.History([]ledger.Event{event}, r.conversation, r.agent)
		if err != nil {
			return provider.Request{}, report, err
		}
		if len(items) == 0 {
			continue
		}
		item, err := project(ctx, event.ID)
		if err != nil {
			return provider.Request{}, report, err
		}
		view.History = append(view.History, item)
	}
	in.MaxTokens = limit
	in.Summary = nil
	in.History = nil
	if len(view.Root) > 0 {
		in.History = append(in.History, view.Root)
	}
	for _, item := range view.History {
		in.History = append(in.History, item.Item)
	}
	request, report = assemble.Assemble(in, count)
	if report.Failure != "" && report.Failure != assemble.TooLarge {
		return provider.Request{}, report, assemble.ErrHistory
	}
	// Select optional context against the published history projection, never
	// the raw events it covers. Freeze real budget drops before further fitting.
	var budgetDrops []assemble.Drop
	omitted := map[string]bool{}
	for _, drop := range report.Dropped {
		if drop.Reason == assemble.Budget {
			budgetDrops = append(budgetDrops, drop)
			omitted[drop.ID] = true
		}
	}
	var packet assemble.ContextPacket
	for _, item := range in.Packet {
		if !omitted[item.ID] {
			packet = append(packet, item)
		}
	}
	in.Packet = packet
	if report.Failure == assemble.TooLarge {
		// Only mandatory history/context remains; allow the fitting pass to
		// reduce the former without carrying oversized optional material.
		in.MaxTokens = int(^uint(0) >> 1)
		request, report = assemble.Assemble(in, count)
		if report.Failure != "" {
			return provider.Request{}, report, assemble.ErrHistory
		}
	}
	view.Request = request
	view.Packet = request.Items[fixed+len(in.History)+1:]
	configuration, _ := json.Marshal(auxiliaryRequest)
	builder := &compaction.Builder{Store: store, Events: r.journal(), Agent: r.agent, Conversation: r.conversation, CurrentInput: current.ID, Model: in.Model, GenerationRevision: fmt.Sprintf("%s:%x", in.Effort, sha256.Sum256(configuration)), Now: time.Now, NextID: func() string { return "summary:" + rand.Text() }}
	builder.Generate = func(ctx context.Context, segment compaction.PassSegment) (compaction.Text, error) {
		// Builder checked the exact coverage/frontier and supplied canonical
		// source ids. Project those ids in their original provider format.
		for i, source := range segment.Records {
			item, err := project(ctx, source.ID)
			if err != nil {
				return compaction.Text{}, err
			}
			segment.Records[i] = item
		}
		return auxiliary.Summarize(ctx, segment)
	}
	c := &conversationCompaction{builder: builder, pending: map[string]bool{}, preparations: r.summaryState().candidates()}
	pass := compaction.Pass{Policy: compaction.Policy{ContextWindow: limit}, Count: count, Summarize: c.summarize, Prepared: c.prepared, Publish: c.publish}
	fitted, err := pass.Fit(ctx, view, false)
	if err != nil && err != compaction.ErrTooLarge {
		return provider.Request{}, report, err
	}
	// The compaction trigger is below the hard admission limit. If protected
	// context alone exceeds that trigger, the real budgeted assembly below
	// still decides whether a request fits. Other fitting failures stay fatal.
	in.MaxTokens = limit
	in.Summary = nil
	in.History = nil
	if len(fitted.View.Root) > 0 {
		in.History = append(in.History, fitted.View.Root)
	}
	for _, item := range fitted.View.History {
		in.History = append(in.History, item.Item)
	}
	request, report = assemble.Assemble(in, count)
	report.Dropped = append(budgetDrops, report.Dropped...)
	if report.Failure != "" {
		return request, report, nil
	}
	c.pass, c.view = pass, fitted.View
	c.initialItems = len(request.Items)
	c.view.Request = request
	c.view.Packet = request.Items[fixed+len(in.History)+1:]
	for _, callback := range bind {
		callback(c)
	}
	return request, report, nil
}
