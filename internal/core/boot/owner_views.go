// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

var progressCapability = protocol.Capability{Name: "progress", Version: "1.0"}

type ownerViews struct {
	mu   sync.Mutex
	last string
	wake chan struct{}
	stop context.CancelFunc
	done chan struct{}
}

func (r *conversationRuntime) notifyOwnerViews() {
	if r.router != nil && r.router.root != r {
		r.router.root.notifyOwnerViews()
		return
	}
	if r.ownerViews != nil {
		select {
		case r.ownerViews.wake <- struct{}{}:
		default:
		}
	}
}
func (r *conversationRuntime) startOwnerViews(ctx context.Context) error {
	r.ownerViews = &ownerViews{wake: make(chan struct{}, 1), done: make(chan struct{})}
	if r.identity.TopicCapabilities == nil {
		r.identity.TopicCapabilities = map[string]protocol.Capability{}
	}
	r.identity.Topics = r.topics
	r.identity.TopicCapabilities["progress"] = progressCapability
	lanes := r.identity.Operations[protocol.KindProgressGet]
	op := lanes
	op.AdditionalCapabilities = []protocol.Capability{progressCapability}
	op.CapabilityFor = func(e protocol.Envelope) protocol.Capability {
		p := e.Payload.(protocol.ProgressGet)
		if p.View != nil && *p.View == "overview" {
			return progressCapability
		}
		return lanes.Capability
	}
	op.Handle = func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
		p := e.Payload.(protocol.ProgressGet)
		if p.View == nil || *p.View == "lanes" {
			return lanes.Handle(ctx, s, e)
		}
		if err := r.authorize(s, nil); err != nil {
			return clientsrv.Result{}, err
		}
		if p.After != nil {
			return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		result, err := r.ownerSnapshot(ctx)
		return clientsrv.Result{Kind: protocol.KindProgress, Payload: result}, err
	}
	r.identity.Operations[protocol.KindProgressGet] = op
	r.identity.Operations[protocol.KindWaitingGet] = clientsrv.Operation{Capability: clientsrv.DecisionsCapability, Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
		if err := r.authorize(s, nil); err != nil {
			return clientsrv.Result{}, err
		}
		names, err := r.ownerAgentNames(ctx)
		if err != nil {
			return clientsrv.Result{}, err
		}
		p := e.Payload.(protocol.WaitingGet)
		opts := waiting.Options{Now: time.Now()}
		if p.Order != nil {
			opts.Order = waiting.Order(*p.Order)
		}
		if p.Limit != nil {
			n, err := p.Limit.Int64()
			if err != nil || n < 0 || n > 10000 {
				return clientsrv.Result{}, waiting.ErrInvalid
			}
			opts.Limit = int(n)
		}
		list, err := r.ownerWaiting(ctx, names, opts)
		if err != nil {
			return clientsrv.Result{}, err
		}
		rows := waitingRows(list)
		total, more := number(list.Total), number(list.More)
		return clientsrv.Result{Kind: protocol.KindWaiting, Payload: protocol.Waiting{Rows: &rows, Total: &total, More: &more}}, nil
	}}
	refresh := r.identity.RefreshTopic
	r.identity.RefreshTopic = func(ctx context.Context, topic string) error {
		if refresh != nil {
			if err := refresh(ctx, topic); err != nil {
				return err
			}
		}
		if topic == "progress" {
			return r.publishOwnerViews(ctx)
		}
		return nil
	}
	watch, cancel := context.WithCancel(ctx)
	r.ownerViews.stop = cancel
	go func() {
		defer close(r.ownerViews.done)
		for {
			changed := r.db.Changed()
			_ = r.publishOwnerViews(watch)
			select {
			case <-watch.Done():
				return
			case <-changed:
			case <-r.ownerViews.wake:
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-watch.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return nil
}
func (r *conversationRuntime) ownerAgentNames(ctx context.Context) (map[string]string, error) {
	agents, err := r.agents.List(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	return names, nil
}
func (r *conversationRuntime) ownerWaiting(ctx context.Context, names map[string]string, opts waiting.Options) (waiting.List, error) {
	name := func(id string) string { return names[id] }
	sources := map[string]waiting.Source{
		"approval": &waiting.ApprovalSource{Service: r.personaAuthority, AgentName: name},
		"question": waiting.RoundSource{AgentName: name},
		"work":     waiting.BlockedWorkSource{AgentName: name},
		"exchange": waiting.ExchangeSource{AgentName: name},
	}
	if r.background != nil {
		sources["run"] = &waiting.RunInputSource{DB: r.db, Owner: r.background.service, AgentName: name}
	}
	if _, err := r.records.Head(ctx); err != nil {
		if !errors.Is(err, records.ErrRecordsChanged) {
			return waiting.List{}, err
		}
		sources["outside"] = outsideWaitingSource{r.person, r.agent, name(r.agent)}
	}
	return waiting.NewWithLive(r.db, sources, map[string]waiting.LiveSource{"worker": workerWaitingSource{r, names}}).Build(ctx, r.person, opts)
}
func waitingRows(list waiting.List) []protocol.WaitingRow {
	rows := []protocol.WaitingRow{}
	for _, entry := range list.Entries {
		v := entry.View
		due := ""
		if !v.Due.IsZero() {
			due = v.Due.Format(time.RFC3339Nano)
		}
		answers := append([]string{}, v.Answers...)
		rows = append(rows, protocol.WaitingRow{Ref: protocol.WaitingRef{Source: entry.Ref.Source, Key: entry.Ref.ID, Revision: strconv.FormatUint(entry.Ref.Revision, 10)}, Line: protocol.WaitingLine{Kind: string(v.Kind), Sentence: v.Sentence, Context: v.Context, Recommendation: v.Recommendation, AgentName: v.AgentName, Answers: answers, FreeText: v.FreeText, Effort: number(int(v.Effort)), Due: due}})
	}
	return rows
}
func number(n int) json.Number { return json.Number(strconv.Itoa(n)) }
func (r *conversationRuntime) ownerSnapshot(ctx context.Context) (protocol.Progress, error) {
	names, err := r.ownerAgentNames(ctx)
	if err != nil {
		return protocol.Progress{}, err
	}
	list, err := r.ownerWaiting(ctx, names, waiting.Options{Now: time.Now(), Limit: 5})
	if err != nil {
		return protocol.Progress{}, err
	}
	var units []progress.Unit
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		units, err = progress.WorkUnits(ctx, tx, r.person, names)
		return err
	})
	if err != nil {
		return protocol.Progress{}, err
	}
	units = append(units, r.liveProgress(ctx, names)...)
	entries := progress.Compute(units)
	rows := []protocol.ProgressRow{}
	for _, entry := range entries {
		v := entry.Line
		line := protocol.ProgressLine{Name: v.Name, Kind: v.Kind, Stage: v.Stage, JustFinished: v.JustFinished, Next: v.Next, Waiting: number(v.Waiting), Depth: number(v.Depth)}
		if v.Percent != nil {
			percent := number(*v.Percent)
			line.Percent = &percent
		}
		rows = append(rows, protocol.ProgressRow{Ref: entry.Ref, Line: line})
	}
	waiting := waitingRows(list)
	more := number(list.More)
	return protocol.Progress{Rows: &rows, Waiting: &waiting, WaitingMore: &more}, nil
}
func (r *conversationRuntime) publishOwnerViews(ctx context.Context) error {
	v := r.ownerViews
	v.mu.Lock()
	defer v.mu.Unlock()
	p, err := r.ownerSnapshot(ctx)
	if err != nil {
		return err
	}
	update := protocol.ProgressUpdate{Rows: p.Rows, Waiting: p.Waiting, WaitingMore: p.WaitingMore}
	text := ownerText(p)
	update.Text = &text
	raw, err := json.Marshal(update)
	if err != nil {
		return err
	}
	if string(raw) == v.last {
		return nil
	}
	if err = r.topics.Publish("progress", clientsrv.TopicEvent{Kind: protocol.KindProgressUpdate, Key: "overview", Data: raw}); err != nil {
		return err
	}
	v.last = string(raw)
	return nil
}
func ownerText(p protocol.Progress) string {
	lines := []string{}
	if p.Waiting != nil {
		for _, row := range *p.Waiting {
			lines = append(lines, row.Line.Sentence)
		}
	}
	if p.WaitingMore != nil && *p.WaitingMore != "0" {
		lines = append(lines, "+"+string(*p.WaitingMore)+" more")
	}
	if p.Rows != nil {
		for _, row := range *p.Rows {
			line := row.Line
			bar := "unknown"
			if line.Percent != nil {
				bar = string(*line.Percent) + "%"
			}
			lines = append(lines, line.Name+" · "+line.Stage+" · "+bar+" · "+line.JustFinished+" → "+line.Next)
		}
	}
	if len(lines) == 0 {
		return "No work in progress"
	}
	return strings.Join(lines, "\n")
}

// Outside edits are one actionable problem, without displaying their paths or
// unaccepted contents. Resolving the repository removes it on the next read.
type outsideWaitingSource struct{ person, agent, name string }

func (s outsideWaitingSource) Candidates(context.Context, *canon.Tx, string) ([]string, error) {
	return []string{"records"}, nil
}
func (s outsideWaitingSource) Read(context.Context, *canon.Tx, string) (waiting.Item, error) {
	return waiting.Item{Person: s.person, Agent: s.agent, Revision: 1, Reason: waiting.SignificantChoice, Blocking: true, View: view.Waiting{Kind: view.Decide, Sentence: "Review changes made outside LINA", AgentName: s.name, Answers: []string{"open"}, Effort: view.ThirtyMinutes}}, nil
}
func (outsideWaitingSource) Done(context.Context, *canon.Tx, string) error { return nil }

// A directly started worker has no plan inventory yet. Its originating request
// supplies the human name; session ids and output stay behind Details. Finishing
// a worker turn does not prove the handed-off work's done condition.
func (r *conversationRuntime) liveProgress(ctx context.Context, names map[string]string) []progress.Unit {
	units := []progress.Unit{}
	for _, session := range r.workers.entries() {
		if _, ok := names[session.origin.agent]; !ok || session.origin.person != r.person {
			continue
		}
		snapshot, err := session.adapter.Snapshot(ctx, session.thread)
		title := strings.TrimSpace(session.input.Body.Text())
		title, _, _ = strings.Cut(title, "\n")
		if title == "" {
			title = "Delegated work"
		}
		u := progress.Unit{ID: "session:" + session.thread.ID, Kind: "project", Name: laneText(title), Active: true, Updated: session.input.RecordedAt.UnixNano()}
		if err == nil {
			u.Waiting = len(snapshot.Pending)
			if snapshot.State == worker.Idle && snapshot.Output != "" {
				u.JustFinished = laneText(title)
			}
		}
		units = append(units, u)
	}
	return units
}
