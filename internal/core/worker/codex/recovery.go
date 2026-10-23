// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func terminal(t wire.V2Turn) *worker.Finish {
	switch t.Status {
	case wire.V2TurnStatusCompleted:
		return &worker.Finish{Status: worker.Done}
	case wire.V2TurnStatusInterrupted:
		return &worker.Finish{Status: worker.Interrupted}
	case wire.V2TurnStatusFailed:
		f := classifyFailure(t.Error)
		return &f
	case wire.V2TurnStatusInProgress:
		return nil
	default:
		return nil
	}
}

// Recover rebinds an existing thread without sending input or changing settings.
// The original turn remains the reconciliation target, even if other turns exist.
func (s *Session) Recover(ctx context.Context, t worker.Thread, workdir string, tools []worker.Tool) (worker.Thread, error) {
	s.control.Lock()
	defer s.control.Unlock()
	if t.ID == "" || t.TurnID == "" || !filepath.IsAbs(workdir) {
		return worker.Thread{}, errors.New("incomplete recovery binding")
	}
	_, declared, err := dynamicTools(tools)
	if err != nil {
		return worker.Thread{}, err
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return worker.Thread{}, errors.New("Codex session already assigned")
	}
	t.ProcessGeneration = s.generation
	t.BindingEpoch++
	s.started = true
	s.tools = declared
	s.snapshot = worker.ThreadSnapshot{Thread: t, State: worker.Unknown, Worker: s.Name(), Version: s.version, Workdir: workdir, Capabilities: s.Capabilities()}
	s.mu.Unlock()
	var response wire.V2ThreadResumeResponse
	if err = s.call(ctx, "thread/resume", wire.V2ThreadResumeParams{ThreadId: t.ID}, &response); err != nil {
		return worker.Thread{}, err
	}
	if response.Thread.ID != t.ID || filepath.Clean(string(response.Cwd)) != filepath.Clean(workdir) {
		return worker.Thread{}, errors.New("Codex recovery assignment mismatch")
	}
	return t, nil
}

// Reconcile reads the exact turn and its complete items. It never starts a turn.
// Commands and extension tools can have effects beyond the checkout: their exit
// status or prose cannot settle those effects. Such items require independent
// reconciliation and conservatively keep EffectsKnown false here.
func (s *Session) Reconcile(ctx context.Context, t worker.Thread) (worker.ThreadSnapshot, error) {
	s.control.Lock()
	defer s.control.Unlock()
	s.mu.Lock()
	err := s.bound(t, true)
	result := s.snapshot
	revision := s.revision
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	result.Finish = nil
	result.EffectsKnown = false
	var response wire.V2ThreadReadResponse
	if err = s.call(ctx, "thread/read", wire.V2ThreadReadParams{ThreadId: t.ID}, &response); err != nil {
		return result, err
	}
	if response.Thread.ID != t.ID {
		return result, errors.New("Codex reconciliation thread mismatch")
	}
	result.State = threadState(response.Thread.Status)
	full := wire.V2TurnItemsView(`"full"`)
	params := wire.V2ThreadTurnsListParams{ThreadId: t.ID, ItemsView: &full}
	cursors := map[string]bool{}
	for page := 0; page < 1000; page++ {
		var turns wire.V2TurnsPage
		if err = s.call(ctx, "thread/turns/list", params, &turns); err != nil {
			return result, err
		}
		for _, turn := range turns.Data {
			if turn.ID != t.TurnID {
				continue
			}
			result.Finish = terminal(turn)
			result.EffectsKnown = result.Finish != nil && turn.Items != nil && (turn.ItemsView == nil || string(*turn.ItemsView) == string(full))
			for _, item := range turn.Items {
				switch {
				case item.AgentMessage != nil:
					result.Output = item.AgentMessage.Text
				case item.UserMessage != nil, item.Plan != nil, item.Reasoning != nil, item.ContextCompaction != nil:
				default:
					result.EffectsKnown = false
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if revision != s.revision || s.bound(t, true) != nil {
				return result, errors.New("Codex changed during reconciliation")
			}
			result.Pending = s.pendingRequests()
			if len(result.Pending) > 0 {
				result.EffectsKnown = false
				result.State = worker.Waiting
			}
			if result.Finish != nil && result.State != worker.Active && result.State != worker.Waiting {
				s.finished[t.TurnID] = true
				result.State = worker.Idle
				s.snapshot.State = worker.Idle
				s.snapshot.Output = result.Output
			} else {
				result.EffectsKnown = false
			}
			return result, nil
		}
		if turns.NextCursor == nil || *turns.NextCursor == "" {
			return result, nil
		}
		if cursors[*turns.NextCursor] {
			return result, errors.New("Codex repeated turns cursor")
		}
		cursors[*turns.NextCursor] = true
		params.Cursor = turns.NextCursor
	}
	return result, errors.New("Codex turns page limit")
}
