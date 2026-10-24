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
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

const (
	ownerViewRefreshInterval = 5 * time.Second
	// ownerViewSettle coalesces a burst of commits into one projection. The
	// publish must land within 250 ms of the commit (work-and-delegation.md#stages),
	// so the window is the least interval between publishes: it leaves room for
	// the projection itself, and an isolated commit is never held for it.
	ownerViewSettle = 100 * time.Millisecond
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
	if err := r.startOwnerControls(); err != nil {
		return err
	}
	r.ownerViews = &ownerViews{wake: make(chan struct{}, 1), done: make(chan struct{})}
	if r.identity.TopicCapabilities == nil {
		r.identity.TopicCapabilities = map[string]protocol.Capability{}
	}
	r.identity.Topics = r.topics
	r.identity.TopicCapabilities["progress"] = progressCapability
	if err := r.wrapOperation(protocol.KindProgressGet, func(lanes clientsrv.Operation) clientsrv.Operation {
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
		return op
	}); err != nil {
		return err
	}
	if err := addOperations(&r.identity, map[protocol.Kind]clientsrv.Operation{protocol.KindWaitingGet: {Capability: clientsrv.DecisionsCapability, Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
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
	}}}); err != nil {
		return err
	}
	r.chainRefresh(func(ctx context.Context, topic string) error {
		if topic == "progress" {
			return r.publishOwnerViews(ctx)
		}
		return nil
	})
	watch, cancel := context.WithCancel(ctx)
	r.ownerViews.stop = cancel
	go func() {
		defer close(r.ownerViews.done)
		timer := time.NewTicker(ownerViewRefreshInterval)
		defer timer.Stop()
		r.runOwnerViews(watch, timer.C)
	}()
	return nil
}

func (r *conversationRuntime) runOwnerViews(watch context.Context, refresh <-chan time.Time) {
	for {
		changed := r.db.Changed()
		published := time.Now()
		_ = r.publishOwnerViews(watch)
		pending := false
		for !pending {
			select {
			case <-watch.Done():
				return
			case <-changed:
				pending = true
			case <-r.ownerViews.wake:
				pending = true
			case <-refresh:
				// External edits have no canon event. Reconcile only while an overview
				// observer is connected; unchanged data still publishes no frame.
				pending = r.topics.HasSubscribers("progress")
			}
		}
		timer := time.NewTimer(ownerViewSettle - time.Since(published))
		select {
		case <-watch.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
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

// ownerWaiting serves the person's waiting list and the actions on it. It may
// clean finished candidates, so it takes canon's writer.
func (r *conversationRuntime) ownerWaiting(ctx context.Context, names map[string]string, opts waiting.Options) (waiting.List, error) {
	return r.waitingList(ctx, names, opts, true)
}

// ownerProjectionWaiting serves the progress and overview projections. They
// publish on every commit, so they read without the writer: queueing a
// projection behind the commits it describes delays the person's next frame.
func (r *conversationRuntime) ownerProjectionWaiting(ctx context.Context, names map[string]string, opts waiting.Options) (waiting.List, error) {
	return r.waitingList(ctx, names, opts, false)
}

func (r *conversationRuntime) waitingList(ctx context.Context, names map[string]string, opts waiting.Options, clean bool) (waiting.List, error) {
	name := func(id string) string { return names[id] }
	sources := map[string]waiting.Source{
		"approval":              &waiting.ApprovalSource{Service: r.personaAuthority, AgentName: name},
		"question":              waiting.RoundSource{AgentName: name},
		"conversation_question": waiting.ConversationQuestionSource{AgentName: name},
		"work":                  waiting.BlockedWorkSource{AgentName: name},
		"exchange":              waiting.ExchangeSource{AgentName: name},
	}
	if r.background != nil {
		sources["run"] = &waiting.RunInputSource{DB: r.db, Owner: r.background.service, AgentName: name}
		sources["run_effect"] = waiting.RunEffectSource{AgentName: name}
	}
	if _, err := r.records.Head(ctx); err != nil {
		if !errors.Is(err, records.ErrRecordsChanged) {
			return waiting.List{}, err
		}
		sources["outside"] = outsideWaitingSource{r.person, r.agent, name(r.agent)}
	}
	service := waiting.NewWithLive(r.db, sources, map[string]waiting.LiveSource{"worker": workerWaitingSource{r, names}, "a2a": a2aWaitingSource{r, names}})
	if clean {
		return service.Build(ctx, r.person, opts)
	}
	return service.BuildProjection(ctx, r.person, opts)
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

// goalTitles reads each goal's own sentence. The execution ledger never holds
// the goal's prose. A definition the person removed, or that no longer parses,
// leaves that goal's name unknown and does not change the other names. A
// failed ledger read still fails the projection.
func (r *conversationRuntime) goalTitles(ctx context.Context) (map[string]string, error) {
	if r.workEngine == nil || r.workEngine.engine == nil {
		return map[string]string{}, nil
	}
	return r.workEngine.engine.GoalSentences(ctx)
}

func (r *conversationRuntime) ownerSnapshot(ctx context.Context) (protocol.Progress, error) {
	p, _, err := r.ownerProjection(ctx)
	return p, err
}
func (r *conversationRuntime) ownerProjection(ctx context.Context) (protocol.Progress, string, error) {
	return r.ownerProjectionOrder(ctx, waiting.Priority)
}
func (r *conversationRuntime) ownerProjectionOrder(ctx context.Context, order waiting.Order) (protocol.Progress, string, error) {
	names, err := r.ownerAgentNames(ctx)
	if err != nil {
		return protocol.Progress{}, "", err
	}
	list, err := r.ownerProjectionWaiting(ctx, names, waiting.Options{Now: time.Now(), Order: order})
	if err != nil {
		return protocol.Progress{}, "", err
	}
	var units []progress.Unit
	titles, err := r.goalTitles(ctx)
	if err != nil {
		return protocol.Progress{}, "", err
	}
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		units, err = progress.WorkUnits(ctx, tx, r.person, names)
		if err != nil {
			return err
		}
		goals, err := progress.Goals(ctx, tx, r.person, names, func(id string) string { return titles[id] })
		if err != nil {
			return err
		}
		units = append(units, goals...)
		return nil
	})
	if err != nil {
		return protocol.Progress{}, "", err
	}
	present := map[string]int{}
	for i, u := range units {
		present[u.ID] = i
	}
	live, err := r.liveProgress(ctx, names, list)
	if err != nil {
		return protocol.Progress{}, "", err
	}
	for _, u := range live {
		if i, exists := present[u.ID]; !exists {
			units = append(units, u)
		} else if u.Waiting > 0 {
			units[i].Waiting = u.Waiting
			units[i].Now = u.Now
			units[i].State = "needs_input"
		}
	}
	background, err := r.backgroundProgress(ctx, names)
	if err != nil {
		return protocol.Progress{}, "", err
	}
	units = append(units, background...)
	entries := progress.Compute(units)
	rows := []protocol.ProgressRow{}
	for _, entry := range entries {
		v := entry.Line
		line := protocol.ProgressLine{Name: v.Name, Kind: v.Kind, Stage: v.Stage, JustFinished: v.JustFinished, Next: v.Next, Waiting: number(v.Waiting), Depth: number(v.Depth)}
		if !v.StartedAt.IsZero() {
			value := v.StartedAt.UTC().Format(time.RFC3339Nano)
			line.StartedAt = &value
		}
		if !v.FinishedAt.IsZero() {
			value := v.FinishedAt.UTC().Format(time.RFC3339Nano)
			line.FinishedAt = &value
		}
		if v.Now != "" {
			value := laneText(v.Now)
			line.Now = &value
		}
		if v.State != "" {
			state := protocol.RunningState(v.State)
			line.State = &state
		}
		if entry.Row {
			row := true
			line.Row = &row
		}
		if v.Percent != nil {
			percent := number(*v.Percent)
			line.Percent = &percent
		}
		if len(v.Stages) > 0 {
			stages := make([]protocol.ProgressStage, 0, len(v.Stages))
			for _, entry := range v.Stages {
				stage := protocol.ProgressStage{Kind: string(entry.Kind), Label: entry.Label, State: string(entry.State)}
				if entry.Number > 0 {
					value := number(entry.Number)
					stage.Number = &value
				}
				if len(entry.Steps) > 0 {
					steps := make([]protocol.ProgressStageStep, 0, len(entry.Steps))
					for _, step := range entry.Steps {
						wire := protocol.ProgressStageStep{Name: step.Name, State: string(step.State)}
						if !step.StartedAt.IsZero() {
							value := step.StartedAt.UTC().Format(time.RFC3339Nano)
							wire.StartedAt = &value
						}
						steps = append(steps, wire)
					}
					stage.Steps = &steps
				}
				stages = append(stages, stage)
			}
			line.Stages = &stages
		}
		rows = append(rows, protocol.ProgressRow{Ref: entry.Ref, Line: line})
	}
	cardOrder := workCardOrder(rows)
	// The running count counts these lines, never their rows, so Core publishes
	// it instead of leaving a client to count tasks (work-and-delegation.md#running-list).
	counted := progress.CountLines(entries)
	counts := protocol.ProgressCounts{Running: number(counted.Running), Waiting: number(counted.Waiting)}
	// Track the complete list before the Home limit. A change below that limit
	// must invalidate an open full waiting view even when Home looks identical.
	full, err := json.Marshal(waitingRows(list))
	if err != nil {
		return protocol.Progress{}, "", err
	}
	count := min(5, len(list.Entries))
	list.More = list.Total - count
	list.Entries = list.Entries[:count]
	waiting := waitingRows(list)
	more := number(list.More)
	return protocol.Progress{Rows: &rows, CardOrder: &cardOrder, Counts: &counts, Waiting: &waiting, WaitingMore: &more}, string(full), nil
}
func (r *conversationRuntime) publishOwnerViews(ctx context.Context) error {
	v := r.ownerViews
	v.mu.Lock()
	defer v.mu.Unlock()
	p, fullWaiting, err := r.ownerProjection(ctx)
	if err != nil {
		return err
	}
	update := protocol.ProgressUpdate{Rows: p.Rows, CardOrder: p.CardOrder, Counts: p.Counts, Waiting: p.Waiting, WaitingMore: p.WaitingMore}
	text := ownerText(p)
	update.Text = &text
	raw, err := json.Marshal(update)
	if err != nil {
		return err
	}
	identity := string(raw) + "\n" + fullWaiting
	if identity == v.last {
		return nil
	}
	if err = r.topics.Publish("progress", clientsrv.TopicEvent{Kind: protocol.KindProgressUpdate, Key: "overview", Data: raw}); err != nil {
		return err
	}
	v.last = identity
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
			name := line.Name
			if line.State != nil {
				name = string(*line.State) + ": " + name
			}
			lines = append(lines, name+" · "+line.Stage+" · "+bar+" · "+line.JustFinished+" → "+line.Next)
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
func (r *conversationRuntime) liveProgress(ctx context.Context, names map[string]string, list waiting.List) ([]progress.Unit, error) {
	if r.background == nil || r.background.sessions == nil {
		return nil, nil
	}
	// Use the running list's copied adapter observations and its acknowledgement
	// rules. No adapter I/O occurs inside the canonical read, and no run/work
	// producers participate in this conversation-worker projection.
	source := r.background.sessions
	if err := source.refresh(ctx); err != nil {
		return nil, err
	}
	snapshot, err := runs.NewRunningList(r.db, map[string]runs.ListSource{"session": source}).Build(ctx, r.person)
	if err != nil {
		return nil, err
	}
	sessions := map[string]*workerSession{}
	for _, session := range r.workers.entries() {
		sessions[session.thread.ID] = session
	}
	pending := map[string]bool{}
	for _, entry := range list.Entries {
		if entry.Ref.Source == "worker" || entry.Ref.Source == "approval" {
			pending[entry.Ref.ID] = true
		}
	}
	grouped := map[string]progress.Unit{}
	// The first observation for a shared task uses the running list's
	// attention-first ordering, independent of registry map iteration.
	for _, observation := range snapshot.Entries {
		session := sessions[observation.Ref.ID]
		if session == nil {
			continue
		}
		if _, ok := names[session.origin.agent]; !ok || session.origin.person != r.person {
			continue
		}
		id := session.thread.TaskID
		if id == "" {
			id = "session:" + session.thread.ID
		}
		u, exists := grouped[id]
		if !exists {
			title := strings.TrimSpace(session.input.Body.Text())
			title, _, _ = strings.Cut(title, "\n")
			if title == "" {
				title = "Delegated work"
			}
			u = progress.Unit{ID: id, Kind: "project", Name: laneText(title), State: observation.View.State, StartedAt: session.started, Now: design.Text(eventLanguage(session.input), "running.state."+observation.View.State), Updated: session.input.RecordedAt.UnixNano()}
		}
		u.Active = u.Active || observation.Group != "landed"
		u.Landed = u.Landed || observation.Group == "landed"
		if !session.started.IsZero() && (u.StartedAt.IsZero() || session.started.Before(u.StartedAt)) {
			u.StartedAt = session.started
		}
		current := session.progressTurn()
		session.mu.Lock()
		if !exists && session.activity != "" && observation.View.State == "in_progress" {
			u.Now = design.Text(eventLanguage(session.input), session.activity)
		}
		if !exists && session.doing != "" && session.doingTurn == current.TurnID && observation.View.State == "in_progress" {
			u.Now = session.doing
		}
		owned := map[string]bool{}
		for id := range session.questions {
			owned[id] = true
		}
		for id := range session.approvals {
			owned[id] = true
		}
		session.mu.Unlock()
		for _, entry := range list.Entries {
			if owned[entry.Ref.ID] && pending[entry.Ref.ID] {
				if u.Waiting == 0 {
					u.Now = design.Text(eventLanguage(session.input), "running.state.needs_input") + " · " + laneText(entry.View.Sentence)
				}
				u.Waiting++
				u.State = "needs_input"
			}
		}
		grouped[id] = u
	}
	units := make([]progress.Unit, 0, len(grouped))
	for _, u := range grouped {
		units = append(units, u)
	}
	return units, nil
}
