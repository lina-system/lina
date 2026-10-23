// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"math"
	"runtime"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) startWorkerDispatch(ctx context.Context) error {
	if r.route == nil {
		return nil
	}
	binary, _ := proc.LookPath("agy")
	codexBinary, _ := proc.LookPath("codex")
	claude, _ := proc.LookPath("claude")
	if binary == "" && codexBinary == "" && claude == "" {
		return nil
	}
	gh, err := proc.LookPath("gh")
	if err != nil {
		return nil
	}
	git, err := proc.LookPath("git")
	if err != nil {
		return nil
	}
	r.workerGitHub, err = ghclient.New(ctx, ghclient.Config{Path: gh, Leaser: r.activities})
	if err != nil {
		return err
	}
	var factories []dispatch.Factory
	if binary != "" {
		factories = append(factories, dispatch.AntigravityFactory(dispatch.Config{Binary: binary, Environment: proc.WorkerEnvironment(), Epoch: r.epoch, Activities: r.activities}))
	}
	if codexBinary != "" {
		factories = append(factories, dispatch.CodexFactory(codexBinary))
	}
	rules, err := dispatch.ReadRules(r.root)
	if err != nil {
		return err
	}
	r.workerDispatch, err = dispatch.Open(ctx, dispatch.Config{Rules: rules, Workers: factories, Bind: r.bindDispatchedWorker, Capacity: func(ctx context.Context) int {
		host, err := goal.MainHost(ctx, r.main, r.root, goal.Resources{CPU: float64(runtime.NumCPU()), MemoryMB: math.MaxInt32, DiskMB: math.MaxInt32}, nil)
		if err != nil {
			return 0
		}
		return goal.Slots(host, nil, goal.Resources{CPU: 1, MemoryMB: 512, DiskMB: 1024}, time.Now())
	}, DB: r.db, Work: r.work, Area: r.workArea, Skills: r.skills, Root: r.root, Main: r.main, Epoch: r.epoch, Binary: binary, ClaudeBinary: claude, Git: git, PR: r.workerGitHub, Environment: proc.WorkerEnvironment(), Activities: r.activities, Manage: func(ctx context.Context, record dispatch.Record) (dispatch.Record, error) {
		raw, _ := json.Marshal(record.TaskDefinition())
		a, err := r.assignCoding(ctx, record.Origin, record.ID, "dispatch_work", record.Definition.Repository, raw)
		if err != nil {
			return record, err
		}
		if err = r.runCodingManager(ctx, a, "assignment", nil); err != nil {
			return record, err
		}
		record.Owner, record.Manager = a.Owner, a.Conversation
		return record, nil
	}, Publish: func(ctx context.Context, record dispatch.Record) error {
		managerVerdict := "unverified"
		if record.Owner != "" {
			var a codingAssignment
			if err := r.db.Read(ctx, func(tx *canon.Tx) error { var err error; a, err = readCoding(ctx, tx, record.ID); return err }); err != nil {
				return err
			}
			// No receipt means the worker never delivered a judgeable result.
			if card, err := r.work.Get(ctx, record.ID); err == nil && card.Receipt != nil {
				managerVerdict, err = r.deliverCoding(ctx, a, record)
				if err != nil {
					return err
				}
				if managerVerdict == "returned" {
					return nil
				}
			}
		}
		view, err := r.router.view(ctx, clientsrv.Session{PersonID: record.Origin.Person}, record.Origin.Agent)
		if err != nil {
			return err
		}
		text := record.WorkerName() + ": " + record.Code
		if record.Version != "" {
			text += " (" + record.Version + "; unverified version)"
		}
		for _, denial := range record.Denied {
			text += "; denied: " + denial.DisplayName
		}
		if record.PR != nil {
			text += fmt.Sprintf("; %s @ %s; Core delivery: %s", record.PR.URL, record.PR.Head, managerVerdict)
		}
		parent, err := view.journal().Get(ctx, strings.TrimPrefix(record.Origin.Turn, "turn:"))
		if err != nil {
			return err
		}
		now := time.Now()
		event, err := view.journal().Get(ctx, "worker-result:"+record.ID)
		if errors.Is(err, ledger.ErrNotFound) {
			event, err = view.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: "worker-result:" + record.ID, ConversationID: record.Origin.Conversation, ParentID: strings.TrimPrefix(record.Origin.Turn, "turn:"), AgentID: record.Origin.Agent, Surface: parent.Surface, ScaffoldKind: "worker_result", OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: text})
		}
		if err != nil {
			return err
		}
		body, speaker := event.Body.Text(), protocol.ConversationSpeakerSystem
		return view.publish(ctx, protocol.KindItemDone, event.ID, protocol.ItemDone{Text: &body, Speaker: &speaker})
	}})
	return err
}

// Reuse the authenticated conversation approval/question bridge. Dispatch owns
// adapter events and lifetime; these entries only route owner decisions.
func (r *conversationRuntime) bindDispatchedWorker(record dispatch.Record, adapter worker.Adapter, thread worker.Thread, packet, workdir string) (func(worker.Event), func()) {
	delivery := newConversationView(r.router.root, record.Origin.Agent, record.Origin.Conversation)
	owner := newConversationView(r.router.root, record.Owner, record.Manager)
	owner.codingDelivery = delivery
	input, _ := delivery.journal().Get(r.ctx, strings.TrimPrefix(record.Origin.Turn, "turn:"))
	e := &workerSession{adapter: adapter, close: func() error { return adapter.Interrupt(context.Background(), thread, worker.UserCancel) }, origin: owner, input: input, thread: thread, packet: packet, workdir: workdir, started: time.Now(), done: make(chan struct{}), approvals: map[string]worker.Event{}, questions: map[string]*workerQuestion{}, active: map[string]lease.Lease{}}
	r.workers.mu.Lock()
	r.workers.sessions[thread.ID] = e
	r.workers.mu.Unlock()
	handle := func(event worker.Event) {
		switch event.Kind {
		case worker.ApprovalEvent:
			e.requestApproval(event)
		case worker.QuestionEvent:
			e.requestQuestion(event)
		case worker.ToolCallEvent:
			e.workTool(event)
		case worker.ProgressEvent:
			if event.Progress.Phase == "server_request_resolved" {
				e.stopRequest(event.Progress.ItemID)
			}
		case worker.FinishEvent:
			e.stopRequests(event.Thread.TurnID)
		case worker.UsageEvent:
		}
	}
	return handle, func() {
		e.stopRequests("")
		r.workers.mu.Lock()
		delete(r.workers.sessions, thread.ID)
		r.workers.mu.Unlock()
		close(e.done)
	}
}
