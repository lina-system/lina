// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	wire "github.com/thisisjun786/lina/third_party/codex-app-server/upstream"
)

func (s *Session) Start(ctx context.Context, l lease.Lease, spec worker.StartSpec) (worker.Thread, error) {
	s.control.Lock()
	defer s.control.Unlock()
	if err := lease.Do(ctx, l, func(context.Context) error { return nil }); err != nil {
		return worker.Thread{}, err
	}
	if err := s.Capabilities().Require(spec.Required...); err != nil {
		return worker.Thread{}, err
	}
	if len(spec.Tools) > 0 {
		return worker.Thread{}, ErrUnsupported
	}
	if !filepath.IsAbs(spec.Workdir) || strings.TrimSpace(spec.TaskPacket) == "" || spec.TaskID == "" || spec.AttemptID == "" {
		return worker.Thread{}, errors.New("worker task needs identity, absolute workdir and task packet")
	}
	if spec.Effort != "" {
		return worker.Thread{}, errors.New("per-task effort selection requires verified assignment support")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return worker.Thread{}, errors.New("Codex session already assigned")
	}
	s.started = true
	s.snapshot = worker.ThreadSnapshot{Thread: worker.Thread{TaskID: spec.TaskID, AttemptID: spec.AttemptID, ProcessGeneration: s.generation, BindingEpoch: 1}, State: worker.Unknown, Worker: s.Name(), Version: s.version, Capabilities: s.Capabilities()}
	s.mu.Unlock()
	p := wire.V2ThreadStartParams{Cwd: &spec.Workdir}
	if spec.Model != "" {
		v := string(spec.Model)
		p.Model = &v
	}
	var response wire.V2ThreadStartResponse
	if err := s.call(ctx, string(wire.ClientRequestThreadStartMethodThreadStart), p, &response); err != nil {
		return worker.Thread{}, err
	}
	if response.Thread.ID == "" || filepath.Clean(string(response.Cwd)) != filepath.Clean(spec.Workdir) || spec.Model != "" && string(spec.Model) != response.Model {
		return worker.Thread{}, errors.New("Codex assignment mismatch")
	}
	s.mu.Lock()
	s.snapshot.Thread.ID = response.Thread.ID
	s.snapshot.Workdir = string(response.Cwd)
	s.snapshot.Model = provider.ModelRef(response.Model)
	s.snapshot.State = worker.Idle
	if response.ReasoningEffort != nil {
		s.snapshot.Effort = provider.Effort(*response.ReasoningEffort)
	}
	if response.InstructionSources != nil {
		for _, v := range *response.InstructionSources {
			s.snapshot.InstructionSources = append(s.snapshot.InstructionSources, string(v))
		}
	}
	s.mu.Unlock()
	if err := s.startTurn(ctx, spec.TaskPacket); err != nil {
		return worker.Thread{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.Thread, nil
}

func input(text string) []wire.V2UserInput {
	return []wire.V2UserInput{{Text: &wire.V2UserInputText{Text: text}}}
}
func (s *Session) startTurn(ctx context.Context, text string) error {
	s.mu.Lock()
	id := s.snapshot.Thread.ID
	s.snapshot.Output = ""
	s.mu.Unlock()
	var response wire.V2TurnStartResponse
	err := s.call(ctx, string(wire.ClientRequestTurnStartMethodTurnStart), wire.V2TurnStartParams{ThreadId: id, Input: input(text)}, &response)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.snapshot.State = worker.Unknown
		return err
	}
	if response.Turn.ID == "" {
		s.snapshot.State = worker.Unknown
		return errors.New("Codex omitted turn id")
	}
	// Notifications can arrive before the reply, including turn/completed.
	if !s.finished[response.Turn.ID] {
		s.snapshot.Thread.TurnID = response.Turn.ID
		s.snapshot.State = worker.Active
	}
	return nil
}

func (s *Session) bound(t worker.Thread, turn bool) error {
	if s.failure != nil {
		return s.failure
	}
	current := s.snapshot.Thread
	if t.ID == "" || t.ID != current.ID || t.ProcessGeneration != current.ProcessGeneration || t.BindingEpoch != current.BindingEpoch || t.TaskID != current.TaskID || t.AttemptID != current.AttemptID || turn && t.TurnID != current.TurnID {
		return errors.New("stale or foreign Codex thread binding")
	}
	return nil
}
func (s *Session) Input(ctx context.Context, t worker.Thread, in worker.Input) error {
	s.control.Lock()
	defer s.control.Unlock()
	if strings.TrimSpace(in.Text) == "" {
		return errors.New("empty worker input")
	}
	s.mu.Lock()
	err := s.bound(t, true)
	state := s.snapshot.State
	s.mu.Unlock()
	if err != nil {
		return err
	}
	switch state {
	case worker.Idle:
		return s.startTurn(ctx, in.Text)
	case worker.Active:
		var response wire.V2TurnSteerResponse
		err = s.call(ctx, string(wire.ClientRequestTurnSteerMethodTurnSteer), wire.V2TurnSteerParams{ThreadId: t.ID, ExpectedTurnId: t.TurnID, Input: input(in.Text)}, &response)
		if err == nil && response.TurnId != t.TurnID {
			err = errors.New("Codex steering turn mismatch")
		}
		return err
	case worker.Waiting, worker.Dead, worker.Unknown:
		return errors.New("Codex thread cannot accept ordinary input")
	}
	return errors.New("unknown worker state")
}
func (s *Session) Interrupt(ctx context.Context, t worker.Thread, why worker.Reason) error {
	switch why {
	case worker.UserCancel, worker.BudgetCap, worker.NodePolicyCancel, worker.StallRecovery:
	default:
		return errors.New("invalid interrupt reason")
	}
	s.control.Lock()
	defer s.control.Unlock()
	s.mu.Lock()
	err := s.bound(t, true)
	state := s.snapshot.State
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if state != worker.Active && state != worker.Waiting {
		return errors.New("Codex has no active turn")
	}
	var response wire.V2TurnInterruptResponse
	return s.call(ctx, string(wire.ClientRequestTurnInterruptMethodTurnInterrupt), wire.V2TurnInterruptParams{ThreadId: t.ID, TurnId: t.TurnID}, &response)
}
func (s *Session) Snapshot(ctx context.Context, t worker.Thread) (worker.ThreadSnapshot, error) {
	s.control.Lock()
	defer s.control.Unlock()
	s.mu.Lock()
	err := s.bound(t, false)
	revision := s.revision
	s.mu.Unlock()
	if err != nil {
		return worker.ThreadSnapshot{}, err
	}
	var response wire.V2ThreadReadResponse
	if err = s.call(ctx, string(wire.ClientRequestThreadReadMethodThreadRead), wire.V2ThreadReadParams{ThreadId: t.ID}, &response); err != nil {
		return worker.ThreadSnapshot{}, err
	}
	if response.Thread.ID != t.ID {
		return worker.ThreadSnapshot{}, errors.New("Codex snapshot thread mismatch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision == s.revision {
		s.snapshot.State = threadState(response.Thread.Status)
		// A summary can briefly remain idle after turn/start was accepted. It
		// must not authorize a second turn before the accepted one is observed.
		if s.snapshot.State == worker.Idle && s.snapshot.Thread.TurnID != "" && !s.finished[s.snapshot.Thread.TurnID] {
			s.snapshot.State = worker.Unknown
		}
		for _, turn := range response.Thread.Turns {
			s.snapshot.Thread.TurnID = turn.ID
		}
	}
	result := s.snapshot
	result.Capabilities = append(worker.CapabilitySet(nil), result.Capabilities...)
	result.InstructionSources = append([]string(nil), result.InstructionSources...)
	return result, nil
}
func threadState(s wire.V2ThreadStatus) worker.ThreadState {
	switch {
	case s.Idle != nil:
		return worker.Idle
	case s.Active != nil:
		if len(s.Active.ActiveFlags) > 0 {
			return worker.Waiting
		}
		return worker.Active
	case s.SystemError != nil:
		return worker.Dead
	case s.NotLoaded != nil, s.Unrecognized != nil:
		return worker.Unknown
	}
	return worker.Unknown
}
func (s *Session) Answer(context.Context, worker.Thread, worker.Answer) error { return ErrUnsupported }
