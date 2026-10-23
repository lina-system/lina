// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/provider"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func (s *Session) notification(raw []byte) error {
	var n wire.ServerNotification
	if err := json.Unmarshal(raw, &n); err != nil {
		return errors.New("invalid Codex notification")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event := worker.Event{Thread: s.snapshot.Thread}
	var thread, turn string
	switch {
	case n.ThreadNameUpdated != nil:
		thread, turn = n.ThreadNameUpdated.Params.ThreadId, s.snapshot.Thread.TurnID
		event.Kind = worker.ProgressEvent
		event.Progress = &worker.Progress{Phase: "doing"}
	case n.TurnPlanUpdated != nil:
		thread, turn = n.TurnPlanUpdated.Params.ThreadId, n.TurnPlanUpdated.Params.TurnId
		if turn != s.snapshot.Thread.TurnID || s.finished[turn] {
			return nil
		}
		event.Kind = worker.ProgressEvent
		event.Progress = &worker.Progress{Phase: "doing"}
	case n.TurnStarted != nil:
		p := n.TurnStarted.Params
		thread, turn = p.ThreadId, p.Turn.ID
		if thread != s.snapshot.Thread.ID {
			return nil
		}
		if turn == "" {
			return errors.New("Codex turn started without id")
		}
		if s.finished[turn] {
			return nil
		}
		if s.snapshot.Thread.TurnID != turn {
			s.clearRequests("")
		}
		s.snapshot.Thread.TurnID = turn
		s.snapshot.State = worker.Active
		event.Kind = worker.ProgressEvent
		event.Progress = &worker.Progress{Phase: "started"}
	case n.TurnCompleted != nil:
		p := n.TurnCompleted.Params
		thread, turn = p.ThreadId, p.Turn.ID
		if thread != s.snapshot.Thread.ID {
			return nil
		}
		if turn == "" {
			return errors.New("Codex turn completed without id")
		}
		if s.finishEvents[turn] {
			return nil
		}
		f := &worker.Finish{}
		switch p.Turn.Status {
		case wire.V2TurnStatusCompleted:
			f.Status = worker.Done
		case wire.V2TurnStatusInterrupted:
			f.Status = worker.Interrupted
		case wire.V2TurnStatusFailed:
			*f = classifyFailure(p.Turn.Error)
		case wire.V2TurnStatusInProgress:
			return errors.New("Codex completed an active turn")
		default:
			return errors.New("unknown Codex turn completion status")
		}
		// Completion can precede the start reply without a turn/started event.
		// Retain its binding so Start returns the same turn as the queued finish.
		if s.startingTurn && (s.snapshot.Thread.TurnID == "" || s.finished[s.snapshot.Thread.TurnID]) {
			s.snapshot.Thread.TurnID = turn
		}
		if s.finishEvents == nil {
			s.finishEvents = map[string]bool{}
		}
		s.finishEvents[turn] = true
		s.finished[turn] = true
		s.clearRequests(turn)
		if s.snapshot.Thread.TurnID == turn {
			s.snapshot.State = worker.Idle
		}
		event.Kind = worker.FinishEvent
		event.Finish = f
	case n.ItemAgentMessageDelta != nil:
		p := n.ItemAgentMessageDelta.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.ProgressEvent
		event.Progress = &worker.Progress{ItemID: p.ItemId, Phase: "delta", Text: p.Delta}
	case n.ItemStarted != nil:
		p := n.ItemStarted.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.ProgressEvent
		phase := "started"
		switch {
		case p.Item.CommandExecution != nil:
			phase = "command"
		case p.Item.FileChange != nil:
			phase = "file_change"
		case p.Item.AgentMessage != nil:
			phase = "message"
		case p.Item.McpToolCall != nil || p.Item.DynamicToolCall != nil:
			phase = "tool"
		}
		event.Progress = &worker.Progress{Phase: phase}
	case n.ItemCompleted != nil:
		p := n.ItemCompleted.Params
		thread, turn = p.ThreadId, p.TurnId
		event.Kind = worker.ProgressEvent
		event.Progress = &worker.Progress{Phase: "item_completed"}
		if p.Item.AgentMessage != nil {
			event.Progress.ItemID = p.Item.AgentMessage.ID
			event.Progress.Text = p.Item.AgentMessage.Text
			if thread == s.snapshot.Thread.ID && turn == s.snapshot.Thread.TurnID {
				s.snapshot.Output = p.Item.AgentMessage.Text
			}
		}
	case n.ThreadTokenUsageUpdated != nil:
		p := n.ThreadTokenUsageUpdated.Params
		thread, turn = p.ThreadId, p.TurnId
		u := p.TokenUsage.Last
		event.Kind = worker.UsageEvent
		event.Usage = &provider.Usage{Input: &u.InputTokens, Cached: &u.CachedInputTokens, Output: &u.OutputTokens, Reasoning: &u.ReasoningOutputTokens}
	case n.AccountRateLimitsUpdated != nil:
		if err := s.quotaUpdated(raw); err != nil {
			return err
		}
		select {
		case s.quotaWake <- struct{}{}:
		default:
		}
		return nil
	case n.ServerRequestResolved != nil:
		return s.resolved(n.ServerRequestResolved.Params)
	case n.ThreadStatusChanged != nil:
		p := n.ThreadStatusChanged.Params
		if p.ThreadId != s.snapshot.Thread.ID {
			return nil
		}
		s.snapshot.State = threadState(p.Status)
		thread, turn = p.ThreadId, s.snapshot.Thread.TurnID
		event.Kind = worker.ProgressEvent
		event.Progress = &worker.Progress{Phase: "status"}
	default:
		return nil // Unknown optional notifications never establish completion.
	}
	if thread == "" || thread != s.snapshot.Thread.ID {
		return nil
	}
	s.doing.Record("codex", raw, time.Now())
	if turn == "" {
		return nil
	} // Metadata before the first turn is carried by its first event.
	s.revision++
	event.Thread = s.snapshot.Thread
	event.Thread.TurnID = turn
	return s.publish(event)
}

// publish is called only by the reader with mu held.
func (s *Session) publish(event worker.Event) error {
	if event.Thread.ID == s.snapshot.Thread.ID && event.Thread.TurnID == s.snapshot.Thread.TurnID {
		if s.snapshot.State == worker.Active || s.snapshot.State == worker.Idle {
			s.doing.Busy(s.snapshot.State == worker.Active, time.Now())
		}
		observation := s.doing.Snapshot()
		if !observation.Since.IsZero() {
			event.Doing = &observation
			s.snapshot.Doing = &observation
		}
	}
	if err := event.Validate(); err != nil {
		return err
	}
	// Reserve one slot for a terminal loss signal, even when nobody is reading.
	if len(s.events) >= cap(s.events)-1 {
		return errors.New("Codex event consumer fell behind; reconcile required")
	}
	s.events <- event
	return nil
}

// CodexErrorInfo is an open, untagged union in the vendored schema. Retain
// unknown values as worker failures; never turn provider error prose into a code.
func classifyFailure(e *wire.V2TurnError) worker.Finish {
	f := worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "worker_failed"}
	if e == nil || e.CodexErrorInfo == nil {
		return f
	}
	var scalar string
	if json.Unmarshal(*e.CodexErrorInfo, &scalar) == nil {
		switch scalar {
		case "usageLimitExceeded", "sessionBudgetExceeded":
			f.Failure = worker.QuotaFailure
		case "rateLimitExceeded":
			f.Failure = worker.RateLimitFailure
		case "unauthorized":
			f.Failure = worker.LoginFailure
		}
	} else {
		var detail map[string]struct {
			HTTPStatusCode *uint16 `json:"httpStatusCode"`
		}
		if json.Unmarshal(*e.CodexErrorInfo, &detail) == nil {
			for name, d := range detail {
				switch name {
				case "httpConnectionFailed", "responseStreamConnectionFailed", "responseStreamDisconnected", "responseTooManyFailedAttempts":
					f.Failure = worker.ConnectionFailure
					if d.HTTPStatusCode != nil {
						switch *d.HTTPStatusCode {
						case 401, 403:
							f.Failure = worker.LoginFailure
						case 429:
							f.Failure = worker.RateLimitFailure
						}
					}
				}
			}
		}
	}
	f.Code = string(f.Failure)
	return f
}
