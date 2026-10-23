// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/skills"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (p *publicationRuntime) start(ctx context.Context) {
	life, cancel := context.WithCancel(ctx)
	p.stop = cancel
	p.done = make(chan struct{})
	p.sweep(life)
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			p.sweep(life)
			select {
			case <-life.Done():
				return
			case <-p.wake:
			case <-ticker.C:
			}
		}
	}()
}
func (p *publicationRuntime) view(ctx context.Context, id string) (*conversationRuntime, error) {
	rec, err := p.store.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	view := newConversationView(p.root, rec.Agent, "a2a:"+rec.Task.ID)
	view.externalTask = id
	// Approvals and questions use the existing eligible owner conversation.
	owner, err := p.root.router.view(ctx, clientsrv.Session{PersonID: p.root.person}, rec.Agent)
	if err != nil {
		return nil, err
	}
	view.codingDelivery = owner
	return view, nil
}
func (p *publicationRuntime) sweep(ctx context.Context) {
	records, err := p.store.Pending(ctx)
	if err != nil {
		p.recovered.Store(false)
		return
	}
	// Repair terminal delivery before accepting or dispatching new work.
	for _, rec := range records {
		if a2a.Terminal(rec.Task.Status.State) {
			if err := p.deliver(ctx, rec); err != nil {
				p.recovered.Store(false)
				return
			}
		}
	}
	p.recovered.Store(true)
	for _, rec := range records {
		if ctx.Err() != nil {
			return
		}
		_ = p.followupNotices(ctx, rec)
		if a2a.Terminal(rec.Task.Status.State) {
			continue
		}
		if err := p.authorize(ctx, rec); err != nil {
			if rec.Phase == "queued" {
				_, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
					state := a2a.Rejected
					if current.CancelRequested {
						state = a2a.Canceled
					}
					a2a.SetState(current, state, time.Now())
					return nil
				})
			} else {
				p.stopTask(ctx, rec)
			}
			continue
		}
		if rec.Phase == "queued" {
			if p.root.route == nil {
				continue
			}
			if err := p.admitTurn(ctx, rec); err != nil {
				continue
			}
		}
		// Read the existing dispatcher by durable origin. This also repairs a crash
		// after tool admission but before the A2A projection saved its work ID.
		rec, err = p.store.Read(ctx, rec.Task.ID)
		if err != nil {
			continue
		}
		work, err := p.dispatched(ctx, rec)
		if err == nil {
			if rec.LocalWork == "" {
				rec, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error { current.LocalWork = work.ID; return nil })
			}
			_ = p.observeWork(ctx, rec, work)
		} else if errors.Is(err, sql.ErrNoRows) && rec.Phase == "executing" {
			var state string
			if p.root.db.Read(ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(ctx, "SELECT state FROM turn_runs WHERE id=?", "turn:a2a-input:"+rec.Task.ID).Scan(&state)
			}) == nil && state != "active" {
				_, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error { a2a.SetState(current, a2a.Failed, time.Now()); return nil })
			}
		}

		var pending int
		if p.root.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM authority_approvals WHERE json_extract(record,'$.State')='pending' AND json_extract(record,'$.TaskID') IN (?,?)", "turn:a2a-input:"+rec.Task.ID, rec.LocalWork).Scan(&pending)
		}) == nil && pending > 0 {
			rec, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
				if !a2a.Terminal(current.Task.Status.State) {
					a2a.SetState(current, a2a.InputRequired, time.Now())
				}
				return nil
			})
		} else if rec.Task.Status.State == a2a.InputRequired && rec.Phase == "executing" {
			rec, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
				if current.Task.Status.State == a2a.InputRequired && current.Phase == "executing" {
					a2a.SetState(current, a2a.Working, time.Now())
				}
				return nil
			})
		}
		_ = p.notice(ctx, rec)
	}
}
func (p *publicationRuntime) admitTurn(ctx context.Context, rec a2a.Record) error {
	view, err := p.view(ctx, rec.Task.ID)
	if err != nil {
		return err
	}
	id := "a2a-input:" + rec.Task.ID
	event, err := view.journal().Get(ctx, id)
	if errors.Is(err, ledger.ErrNotFound) {
		event, err = view.journal().Admit(ctx, guard.EntryForwarded, publicationInput(rec, time.Now()))
	}
	if err != nil {
		return err
	}
	row, err := inbox.New(p.root.db).Admit(ctx, inbox.Input{Key: inbox.Key{Conversation: view.conversation, Source: "a2a", NativeID: rec.Task.ID}, Body: []byte(event.Body.Text())})
	if err != nil {
		return err
	}
	_, err = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error { current.InputEvent = id; current.InboxID = row.ID; return nil })
	if err != nil {
		return err
	}
	claimed, err := p.store.Claim(ctx, rec.Task.ID)
	if err != nil || !claimed {
		return err
	}
	if err = view.submitInput(event, row.ID, false, view); err != nil {
		_, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
			current.Phase = "unknown"
			a2a.SetState(current, a2a.Unspecified, time.Now())
			return nil
		})
	}
	return err
}
func (p *publicationRuntime) dispatched(ctx context.Context, rec a2a.Record) (dispatch.Record, error) {
	var d dispatch.Record
	var raw []byte
	err := p.root.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE turn_id=?", "turn:a2a-input:"+rec.Task.ID).Scan(&raw)
	})
	if err == nil {
		err = json.Unmarshal(raw, &d)
	}
	return d, err
}
func (p *publicationRuntime) stopTask(ctx context.Context, rec a2a.Record) {
	p.root.lanes.Stop("a2a:"+rec.Task.ID, turn.Interrupt{Issuer: turn.Person})
	d, err := p.dispatched(ctx, rec)
	state := a2a.Unspecified
	if err == nil && p.root.workerDispatch != nil {
		_ = p.root.workerDispatch.Cancel(ctx, d.ID)
		for _, session := range p.root.workers.entries() {
			if session.thread.TaskID == d.ID {
				stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				_ = session.adapter.Interrupt(stopCtx, session.thread, worker.UserCancel)
				cancel()
			}
		}
		// The dispatcher's recorded terminal observation, rather than Cancel's
		// acknowledgment, decides whether execution has stopped.
		if d.State == "finished" && (d.Code == "cancelled" || d.Snapshot != nil && d.Snapshot.Finish != nil && d.Snapshot.EffectsKnown && d.Snapshot.Finish.Status == worker.Interrupted) {
			state = a2a.Canceled
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		var turnState string
		e := p.root.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT state FROM turn_runs WHERE id=?", "turn:a2a-input:"+rec.Task.ID).Scan(&turnState)
		})
		if e == nil && turnState != "active" {
			state = a2a.Failed
		}
	}
	_, _ = p.store.Update(ctx, rec.Task.ID, func(current *a2a.Record) error {
		if !a2a.Terminal(current.Task.Status.State) {
			current.Phase = "unknown"
			a2a.SetState(current, state, time.Now())
		}
		return nil
	})
}
func (p *publicationRuntime) notice(ctx context.Context, rec a2a.Record) error {
	view, err := p.root.router.view(ctx, clientsrv.Session{PersonID: p.root.person}, rec.Agent)
	if err != nil {
		return err
	}
	id := "a2a-notice:" + rec.Task.ID + ":" + rec.Task.Status.State
	if a2aPublished(view, id) {
		return nil
	}
	event, err := view.journal().Get(ctx, id)
	if errors.Is(err, ledger.ErrNotFound) {
		now := time.Now()
		labels := map[string]string{a2a.Submitted: "received", a2a.Working: "running", a2a.Completed: "completed", a2a.Failed: "failed", a2a.Canceled: "stopped", a2a.Rejected: "not started", a2a.InputRequired: "waiting for your decision", a2a.AuthRequired: "needs sign-in", a2a.Unspecified: "outcome unknown"}
		text := "External task " + rec.Definition.Name + " from " + rec.Principal.Subject + " (" + rec.Principal.Client + "): " + labels[rec.Task.Status.State] + "."
		event, err = view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: id, ConversationID: rec.Conversation, AgentID: rec.Agent, Surface: ledger.Surface{Kind: "tui"}, ScaffoldKind: "worker_result", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
	}
	if err != nil {
		return err
	}
	text, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
	return view.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker})
}
func (r *conversationRuntime) authorizePublishedWorker(ctx context.Context, d dispatch.Record) (bool, error) {
	id, ok := strings.CutPrefix(d.Origin.Turn, "turn:a2a-input:")
	if !ok {
		return true, nil
	}
	if r.publications == nil {
		return false, a2a.ErrDenied
	}
	rec, err := r.publications.store.Read(ctx, id)
	if err != nil {
		return false, err
	}
	if err = r.publications.authorize(ctx, rec); err != nil {
		return false, nil
	}
	definition := a2a.WorkDefinition(rec)
	definition.Repository, err = filepath.EvalSymlinks(definition.Repository)
	if err != nil {
		return false, err
	}
	definition.Skill, err = skills.MethodID(definition.Kind, definition.Skill)
	if err != nil {
		return false, err
	}
	expected, _ := json.Marshal(definition)
	actual, _ := json.Marshal(d.Definition)
	return a2a.ArgumentsEqual(expected, actual), nil
}

func (r *conversationRuntime) publishedWorkerDeadline(ctx context.Context, d dispatch.Record) (time.Time, error) {
	id, ok := strings.CutPrefix(d.Origin.Turn, "turn:a2a-input:")
	if !ok {
		return time.Time{}, nil
	}
	if r.publications == nil {
		return time.Time{}, a2a.ErrDenied
	}
	rec, err := r.publications.store.Read(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(min(rec.Deadline, rec.Principal.Expires), 0), nil
}

// Supplementary remote input is durable untrusted context for the owner's
// pending question. It cannot replace the fixed task or answer an approval.
func (p *publicationRuntime) followupNotices(ctx context.Context, rec a2a.Record) error {
	if len(rec.Task.History) < 2 {
		return nil
	}
	view, err := p.root.router.view(ctx, clientsrv.Session{PersonID: p.root.person}, rec.Agent)
	if err != nil {
		return err
	}
	for i, message := range rec.Task.History[1:] {
		id := "a2a-followup:" + rec.Task.ID + ":" + strconv.Itoa(i+1)
		if a2aPublished(view, id) {
			continue
		}
		event, err := view.journal().Get(ctx, id)
		if errors.Is(err, ledger.ErrNotFound) {
			raw, _ := json.Marshal(message)
			now := time.Now()
			event, err = view.journal().Admit(ctx, guard.EntryForwarded, ledger.Input{ID: id, Locator: "a2a:" + rec.Publication + ":" + rec.Task.ID, ConversationID: rec.Conversation, AgentID: rec.Agent, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "External task input for " + rec.Definition.Name + ". This is not an approval.\n" + string(raw)})
		}
		if err != nil {
			return err
		}
		text, speaker := event.Body.Text(), protocol.ConversationSpeakerForwarded
		if err = view.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &text, Speaker: &speaker}); err != nil {
			return err
		}
	}
	return nil
}

func a2aPublished(view *conversationRuntime, key string) bool {
	view.publicationMu.Lock()
	defer view.publicationMu.Unlock()
	return view.positioned[key]
}

func publicationInput(rec a2a.Record, now time.Time) ledger.Input {
	body, _ := json.Marshal(rec.Task.History[0])
	return ledger.Input{ID: "a2a-input:" + rec.Task.ID, Locator: "a2a:" + rec.Publication + ":" + rec.Task.ID, ConversationID: "a2a:" + rec.Task.ID, AgentID: rec.Agent, Surface: ledger.Surface{Kind: "a2a"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: string(body)}
}
