// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) startOwnerControls() {
	r.identity.Operations[protocol.KindProgressDigestGet] = clientsrv.Operation{Capability: progressCapability, Handle: r.progressDigest}
	if r.background == nil {
		return
	}
	r.identity.Operations[protocol.KindFollowUp] = clientsrv.Operation{Capability: runningCapability, Handle: r.followRun}
	r.identity.Operations[protocol.KindMultiTaskControl] = clientsrv.Operation{Capability: runningCapability, Handle: r.stopAllRuns}
}
func (r *conversationRuntime) progressDigest(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	request := e.Payload.(protocol.ProgressDigestGet)
	order := waiting.Priority
	if request.Order != nil {
		order = waiting.Order(*request.Order)
	}
	if order != waiting.Priority && order != waiting.Lightest {
		return clientsrv.Result{}, malformedManagement()
	}
	p, _, err := r.ownerProjectionOrder(ctx, order)
	if err != nil {
		return clientsrv.Result{}, err
	}
	lines := []string{}
	for _, row := range *p.Waiting {
		lines = append(lines, row.Line.Sentence)
	}
	entries := []progress.Entry{}
	for _, row := range *p.Rows {
		v := row.Line
		line := progress.Line{Name: v.Name, Stage: v.Stage, JustFinished: v.JustFinished, Next: v.Next}
		if v.State != nil {
			line.State = string(*v.State)
		}
		entries = append(entries, progress.Entry{Line: line})
	}
	d := progress.Digest(lines, entries)
	omitted, _ := p.WaitingMore.Int64()
	d.Total += int(omitted)
	d.More += int(omitted)
	return clientsrv.Result{Kind: protocol.KindProgressDigest, Payload: protocol.ProgressDigest{Lines: d.Lines, Total: number(d.Total), More: number(d.More), Link: "lina://progress"}}, nil
}
func (r *conversationRuntime) controlledRun(ctx context.Context, s clientsrv.Session, id string, conversation *string) (runs.Record, error) {
	if err := r.authorize(s, nil); err != nil {
		return runs.Record{}, err
	}
	if strings.TrimSpace(id) == "" || conversation != nil && *conversation == "" {
		return runs.Record{}, malformedManagement()
	}
	if r.background == nil {
		return runs.Record{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	record, err := r.background.service.Get(ctx, id)
	if err != nil || record.Origin.Person != s.PersonID || conversation != nil && record.Origin.Conversation != *conversation {
		return runs.Record{}, outsideManagement()
	}
	return record, nil
}
func (r *conversationRuntime) followRun(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	return r.followRunWithSource(ctx, s, e, nil)
}
func (r *conversationRuntime) followRunWithSource(ctx context.Context, s clientsrv.Session, e protocol.Envelope, source json.RawMessage) (clientsrv.Result, error) {
	p := e.Payload.(protocol.FollowUp)
	if strings.TrimSpace(p.Text) == "" || utf8.RuneCountInString(p.Text) > 16384 {
		return clientsrv.Result{}, malformedManagement()
	}
	if workerReference(p.RunID) {
		err := r.ownerWorkerControl(ctx, s, p.RunID, p.ConversationID, e.MessageID, p.Text)
		return clientsrv.Result{Kind: protocol.KindFollowUp, Effect: protocol.EffectApplied, Payload: p}, err
	}
	record, err := r.controlledRun(ctx, s, p.RunID, p.ConversationID)
	if err != nil {
		return clientsrv.Result{}, err
	}
	if err = r.background.service.FollowScoped(ctx, record.ID, e.MessageID, p.Text, source); err != nil {
		return clientsrv.Result{}, err
	}
	r.background.notify()
	return clientsrv.Result{Kind: protocol.KindFollowUp, Effect: protocol.EffectApplied, Payload: p}, nil
}
func (r *conversationRuntime) stopAllRuns(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	p := e.Payload.(protocol.MultiTaskControl)
	if p.Action != "stop_all" || p.Count != nil || p.ConversationID != nil && *p.ConversationID == "" {
		return clientsrv.Result{}, malformedManagement()
	}
	if r.background == nil {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	records, err := r.runRecords(ctx)
	if err != nil {
		return clientsrv.Result{}, err
	}
	// Fence queued work before interrupts can report completion and free slots.
	count, err := r.stopScheduled(ctx, s.PersonID, p.ConversationID, "")
	if err != nil {
		return clientsrv.Result{}, err
	}
	for _, record := range records {
		if record.Origin.Person != s.PersonID || record.State == runs.Landed || p.ConversationID != nil && record.Origin.Conversation != *p.ConversationID {
			continue
		}
		if err = r.background.service.Stop(ctx, record.ID); err != nil {
			return clientsrv.Result{}, err
		}
		count++
	}
	for _, entry := range r.workers.entries() {
		if entry.origin.person != s.PersonID || !workerControlConversation(entry.origin, p.ConversationID) {
			continue
		}
		snapshot, err := entry.adapter.Snapshot(ctx, entry.thread)
		if err != nil {
			return clientsrv.Result{}, err
		}
		if snapshot.State != worker.Active && snapshot.State != worker.Waiting {
			continue
		}
		if err = r.ownerWorkerControl(ctx, s, "session:"+entry.thread.ID, p.ConversationID, e.MessageID, ""); err != nil {
			return clientsrv.Result{}, err
		}
		if !entry.scheduled {
			count++
		}
	}
	r.background.notify()
	n := number(count)
	p.Count = &n
	return clientsrv.Result{Kind: protocol.KindMultiTaskControl, Effect: protocol.EffectApplied, Payload: p}, nil
}
