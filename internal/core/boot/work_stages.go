// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Only scoped canonical steps can name a session here. Observations happen
// after the canon read and contain display words, never process/thread ids.
func (r *conversationRuntime) stageWorkers(ctx context.Context, units []progress.Unit) (map[string][]protocol.ProgressStageWorker, error) {
	registry := r.liveWorkers
	if r.workEngine != nil && r.workEngine.sessions != nil {
		registry = r.workEngine.sessions
	}
	out := map[string][]protocol.ProgressStageWorker{}
	for _, u := range units {
		for _, stage := range u.Stages {
			for _, step := range stage.Steps {
				if step.Session == "" || step.State != progress.StepWorking && step.State != progress.StepNeedsInput {
					continue
				}
				if _, read := out[step.Session]; read {
					continue
				}
				out[step.Session] = nil
				readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				session, snapshot, live, err := registry.Observe(readCtx, step.Session)
				if !live {
					session, snapshot, live, err = r.activeStageWorker(readCtx, step.Session, u.Agent)
				}
				cancel()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if !live {
					continue
				}
				state := protocol.RunningStateUnknown
				if err == nil && snapshot.Thread.ID == session.Thread.ID && snapshot.Thread.TurnID == session.Thread.TurnID {
					switch snapshot.State {
					case worker.Dead:
						continue
					case worker.Active:
						state = protocol.RunningStateInProgress
					case worker.Idle, worker.Waiting:
						state = protocol.RunningStateWaiting
					case worker.Unknown:
					}
					if snapshot.State == worker.Waiting {
						for _, request := range snapshot.Pending {
							switch request.Method {
							case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request", "session/request_permission":
								state = protocol.RunningStateNeedsInput
							}
						}
					}
				}
				name := laneText(session.Worker)
				switch session.Worker {
				case "codex", "claude", "antigravity":
					name = workerLabel(session.Worker)
				}
				if name == "" {
					continue
				}
				out[step.Session] = []protocol.ProgressStageWorker{{Name: name, State: state}}
			}
		}
	}
	return out, nil
}

// Dispatch owns an active adapter until it hands the finished turn to Live.
// Use its trusted action binding, not the manager persona or a display name.
func (r *conversationRuntime) activeStageWorker(ctx context.Context, handle, agent string) (worker.Session, worker.ThreadSnapshot, bool, error) {
	registry := r.workers
	if registry == nil {
		return worker.Session{}, worker.ThreadSnapshot{}, false, nil
	}
	for _, e := range registry.entries() {
		if e.goalSession != handle || e.goalPerson != r.person || e.goalAgent != agent {
			continue
		}
		thread := e.progressTurn()
		session := worker.Session{ID: handle, Worker: e.goalWorker, Started: e.started, Thread: thread}
		snapshot, err := e.adapter.Snapshot(ctx, thread)
		registry.mu.Lock()
		current := registry.sessions[e.thread.ID] == e
		registry.mu.Unlock()
		select {
		case <-e.done:
			current = false
		default:
		}
		// A new turn is a new observation, even while the same bridge survives.
		if e.progressTurn() != thread {
			current = false
		}
		return session, snapshot, current, err
	}
	return worker.Session{}, worker.ThreadSnapshot{}, false, nil
}
