// SPDX-License-Identifier: Apache-2.0
// Package antigravity adapts the person's installed agy headless worker.
package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

var ErrUnsupported = errors.New("antigravity operation is unsupported")
var ErrThread = errors.New("antigravity thread does not match current binding")

type Config struct {
	Binary      string
	Environment []string
	Epoch       uint64
	Leaser      lease.Leaser
}

// One adapter owns one task attempt. A result ends its turn; Close reaps the
// remaining process tree. It never installs settings or grants tool permission.
type Adapter struct {
	finish     *worker.Finish
	effectSeen bool
	cfg        Config
	owner      *proc.Owner
	mu         sync.Mutex
	started    bool
	thread     worker.Thread
	spec       worker.StartSpec
	state      worker.ThreadState
	version    string
	result     Result
	cancel     context.CancelFunc
	done       chan struct{}
	events     chan worker.Event
}

var _ worker.Adapter = (*Adapter)(nil)

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg, owner: proc.NewOwner(), events: make(chan worker.Event, 32)}
}
func (*Adapter) Name() string { return "antigravity" }
func (*Adapter) Capabilities() worker.CapabilitySet {
	return worker.CapabilitySet{worker.StreamProgress, worker.ReportUsage, worker.SignalCompletion}
}
func (a *Adapter) Events() <-chan worker.Event                      { return a.events }
func (*Adapter) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (a *Adapter) Version(ctx context.Context) (string, error) {
	if a.cfg.Leaser == nil {
		return "", lease.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	l, err := a.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "worker version"})
	if err != nil {
		return "", err
	}
	defer l.Release()
	var out limitedOutput
	if err = a.owner.Run(ctx, l, proc.Command{Path: a.cfg.Binary, Args: []string{"--version"}, Env: a.cfg.Environment, Stdout: &out, Stderr: io.Discard}); err != nil {
		return "", err
	}
	version := strings.TrimSpace(out.String())
	if version == "" || len(version) > 4096 {
		return "", errors.New("invalid worker version")
	}
	a.mu.Lock()
	a.version = version
	a.mu.Unlock()
	return version, nil
}
func (a *Adapter) Start(ctx context.Context, l lease.Lease, s worker.StartSpec) (worker.Thread, error) {
	if err := a.Capabilities().Require(s.Required...); err != nil {
		return worker.Thread{}, err
	}
	if !filepath.IsAbs(a.cfg.Binary) || !filepath.IsAbs(s.Workdir) || s.TaskID == "" || s.AttemptID == "" || strings.TrimSpace(s.TaskPacket) == "" || a.cfg.Epoch == 0 {
		return worker.Thread{}, errors.New("invalid Antigravity start")
	}
	if s.Effort != "" || len(s.Tools) > 0 {
		return worker.Thread{}, ErrUnsupported
	}
	// Validate the lease before reserving or launching the child.
	if err := lease.Do(ctx, l, func(context.Context) error { return nil }); err != nil {
		return worker.Thread{}, err
	}
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return worker.Thread{}, errors.New("adapter already started")
	}
	a.started = true
	a.spec = s
	a.state = worker.Active
	run, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.done = make(chan struct{})
	a.mu.Unlock()
	stdout, out := io.Pipe()
	ready := make(chan error, 1)
	processDone := make(chan error, 1)
	args := []string{"--output-format", "stream-json"}
	if s.Model != "" {
		args = append(args, "--model", string(s.Model))
	}
	go func() {
		err := a.owner.Run(run, l, proc.Command{Path: a.cfg.Binary, Args: args, Dir: s.Workdir, Env: a.cfg.Environment, Stdin: strings.NewReader(s.TaskPacket), Stdout: out, Stderr: io.Discard})
		out.CloseWithError(err)
		processDone <- err
	}()
	go func() {
		defer close(a.done)
		defer close(a.events)
		defer stdout.Close()
		a.read(run, stdout, ready)
		// Close the pipe before joining on any protocol error. Otherwise the child's
		// writer can hold proc.Run while it waits for a reader that has returned.
		stdout.Close()
		<-processDone
		a.mu.Lock()
		a.state = worker.Dead
		a.mu.Unlock()
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		cancel()
		return worker.Thread{}, errors.New("Antigravity initialization timed out")
	case err := <-ready:
		if err != nil {
			cancel()
			return worker.Thread{}, err
		}
		a.mu.Lock()
		thread := a.thread
		a.mu.Unlock()
		return thread, nil
	case <-ctx.Done():
		cancel()
		return worker.Thread{}, ctx.Err()
	}
}
func (a *Adapter) matches(t worker.Thread) bool { return a.started && t == a.thread && t.ID != "" }
func (a *Adapter) Input(ctx context.Context, t worker.Thread, _ worker.Input) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.matches(t) {
		return ErrThread
	}
	return ErrUnsupported
}
func (a *Adapter) Answer(ctx context.Context, t worker.Thread, _ worker.Answer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.matches(t) {
		return ErrThread
	}
	return ErrUnsupported
}
func (a *Adapter) Interrupt(ctx context.Context, t worker.Thread, why worker.Reason) error {
	switch why {
	case worker.UserCancel, worker.BudgetCap, worker.NodePolicyCancel, worker.StallRecovery:
	default:
		return ErrUnsupported
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.matches(t) {
		return ErrThread
	}
	a.cancel()
	return nil
}
func (a *Adapter) Snapshot(ctx context.Context, t worker.Thread) (worker.ThreadSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.matches(t) {
		return worker.ThreadSnapshot{}, ErrThread
	}
	var finish *worker.Finish
	if a.finish != nil {
		copy := *a.finish
		finish = &copy
	}
	return worker.ThreadSnapshot{Finish: finish, EffectsKnown: finish != nil && !a.effectSeen, Thread: a.thread, State: a.state, Worker: a.Name(), Version: a.version, Workdir: a.spec.Workdir, Model: a.spec.Model, Capabilities: a.Capabilities(), Output: a.result.Response}, nil
}
func (a *Adapter) Result() Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.result
	r.DeniedActions = append([]DeniedAction(nil), r.DeniedActions...)
	return r
}
func (a *Adapter) Close(ctx context.Context) error {
	a.mu.Lock()
	if a.cancel != nil {
		a.cancel()
	}
	done := a.done
	a.mu.Unlock()
	if err := a.owner.Close(ctx); err != nil {
		return err
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (a *Adapter) emit(ctx context.Context, e worker.Event) bool {
	a.mu.Lock()
	e.Thread = a.thread
	a.mu.Unlock()
	select {
	case a.events <- e:
		return true
	case <-ctx.Done():
		return false
	}
}
func (a *Adapter) read(ctx context.Context, r io.Reader, ready chan<- error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	initialized, terminal := false, false
	defer func() {
		if !initialized {
			ready <- errors.New("Antigravity did not initialize")
			a.cancel()
			return
		}
		if !terminal {
			a.emit(ctx, worker.Event{Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: "missing_result"}})
			a.cancel()
		}
	}()
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			return
		}
		switch f.Event {
		case "init":
			if initialized || f.ConversationID == "" || f.Init == nil || filepath.Clean(f.Init.CWD) != filepath.Clean(a.spec.Workdir) || a.spec.Model != "" && f.Init.Model != string(a.spec.Model) {
				return
			}
			a.mu.Lock()
			a.thread = worker.Thread{ID: f.ConversationID, TurnID: a.spec.AttemptID, TaskID: a.spec.TaskID, AttemptID: a.spec.AttemptID, ProcessGeneration: 1, BindingEpoch: a.cfg.Epoch}
			a.mu.Unlock()
			initialized = true
			ready <- nil
		case "step_update":
			if !initialized || f.Step == nil || f.Step.ConversationID != a.thread.ID {
				return
			}
			text := f.Step.Text
			if f.Step.ToolName != "" {
				a.mu.Lock()
				a.effectSeen = true
				a.mu.Unlock()
				text = f.Step.ToolName
			}
			if !a.emit(ctx, worker.Event{Kind: worker.ProgressEvent, Progress: &worker.Progress{ItemID: fmt.Sprint(f.Step.Index), Phase: f.Step.State, Text: text}}) {
				return
			}
		case "result":
			if !initialized || f.Result == nil || f.Result.ConversationID != a.thread.ID || f.Result.Status == "" {
				return
			}
			result := *f.Result
			finish := worker.Finish{Status: worker.Done}
			if len(result.DeniedActions) > 0 {
				finish = worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "denied_actions"}
			} else if result.Status != "SUCCESS" {
				finish = worker.Finish{Status: worker.Failed, Failure: failureKind(result.Error), Code: "result_error"}
			}
			if result.Usage != nil {
				usage := provider.Usage{Input: result.Usage.Input, Output: result.Usage.Output, Cached: result.Usage.Cached, Reasoning: result.Usage.Reasoning}
				if !usage.Valid() {
					return
				}
				if !a.emit(ctx, worker.Event{Kind: worker.UsageEvent, Usage: &usage}) {
					return
				}
			}
			a.mu.Lock()
			a.result = result
			a.finish = &finish
			a.state = worker.Idle
			a.mu.Unlock()
			terminal = true
			a.emit(ctx, worker.Event{Kind: worker.FinishEvent, Finish: &finish})
			// A terminal result is delivered before EOF. Keep draining until Close or
			// exit so a child cannot block on an unread output pipe.
			_, _ = io.Copy(io.Discard, r)
			return
		default:
			return // Unrecognized observations cannot silently become completion.
		}
	}
}
func failureKind(message string) worker.FailureKind {
	switch {
	case strings.Contains(message, "RESOURCE_EXHAUSTED"):
		return worker.QuotaFailure
	case strings.Contains(message, "UNAUTHENTICATED"):
		return worker.LoginFailure
	case strings.Contains(message, "RATE_LIMIT"):
		return worker.RateLimitFailure
	default:
		return worker.WorkerFailure
	}
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, errors.New("worker output limit")
	}
	return b.Buffer.Write(p)
}
