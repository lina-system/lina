// SPDX-License-Identifier: Apache-2.0

// Package tools provides the single validated, authorized tool dispatch path.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
)

var (
	ErrInvalid          = errors.New("invalid_tool_call")
	ErrApprovalRequired = errors.New("approval_required")
	ErrInProgress       = errors.New("effect_in_progress")
	ErrKeyConflict      = errors.New("idempotency_key_conflict")
	ErrUnknown          = errors.New("effect_unknown")
)

type State string

const (
	None      State = "none"
	Refused   State = "refused"
	Failed    State = "failed"
	Applied   State = "applied"
	Cancelled State = "cancelled"
	Unknown   State = "unknown"
)

type Placement string

const (
	Core Placement = "core"
	Node Placement = "node"
)

// Input is computed by Core's registered schema validator, not accepted as a
// request field. Arguments must be validated JSON with defaults resolved.
type Input struct {
	Arguments                     json.RawMessage
	Target, What, Where, ScopeKey string
	Write, Undoable, Network      bool
}
type Schema func(json.RawMessage) (Input, error)
type Spec struct {
	Name, Plugin string
	Schema       Schema
	Classify     Classifier
	// Outward for plugins is derived only from the service profile. Server
	// descriptions and annotations must never populate this field.
	Outward   bool
	Conc      int
	Deadline  time.Duration
	Placement Placement
}
type Call struct {
	Key, Name, Account string
	Auth               grant.Request
	Args               json.RawMessage
}
type Result struct {
	State    State
	Body     json.RawMessage
	Revision string
}
type Ticket struct {
	Key    string
	Replay bool
	Result Result
}
type Gate interface {
	Begin(context.Context, Call, Spec, Input) (Ticket, error)
	Finish(context.Context, Ticket, Result) error
}
type Handler func(context.Context, lease.Lease, Input) (Result, error)
type entry struct {
	spec    Spec
	handler Handler
	slots   chan struct{}
}
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*entry
	gate    Gate
	leaser  lease.Leaser
}

func NewRegistry(gate Gate, leaser lease.Leaser) *Registry {
	return &Registry{entries: make(map[string]*entry), gate: gate, leaser: leaser}
}
func (r *Registry) Register(s Spec, h Handler) error {
	if s.Name == "" || s.Schema == nil || s.Conc < 1 || s.Deadline <= 0 || (s.Placement != Core && s.Placement != Node) || h == nil {
		return ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[s.Name]; ok {
		return ErrInvalid
	}
	r.entries[s.Name] = &entry{s, h, make(chan struct{}, s.Conc)}
	return nil
}

// Dispatch bounds the caller's wait even if an adapter ignores cancellation.
// Such an adapter retains its lease and concurrency slot until it actually exits;
// its effect remains unknown, and a late result never turns it into success.
func (r *Registry) Dispatch(ctx context.Context, c Call) (Result, error) {
	r.mu.RLock()
	e := r.entries[c.Name]
	r.mu.RUnlock()
	if e == nil || r.gate == nil || r.leaser == nil || c.Key == "" || !json.Valid(c.Args) {
		return Result{State: Refused}, ErrInvalid
	}
	in, err := e.spec.Schema(append(json.RawMessage(nil), c.Args...))
	if err != nil {
		return Result{State: Refused}, err
	}
	if in.Target == "" || in.What == "" || in.Where == "" || in.ScopeKey == "" || !json.Valid(in.Arguments) {
		return Result{State: Refused}, ErrInvalid
	}
	in.Arguments = append(json.RawMessage(nil), in.Arguments...)
	c.Auth.Tool = e.spec.Name
	c.Auth.Target = in.Target
	c.Auth.Write = in.Write
	ctx, cancel := context.WithTimeout(ctx, e.spec.Deadline)
	defer cancel()
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return Result{State: Refused}, ctx.Err()
	}
	owned := true
	defer func() {
		if owned {
			<-e.slots
		}
	}()
	class := lease.Live
	if in.Write || e.spec.Outward {
		class = lease.Hard
	}
	l, err := r.leaser.Acquire(ctx, lease.Activity{Class: class, Reason: "tool"})
	if err != nil {
		return Result{State: Refused}, err
	}
	defer func() {
		if owned {
			l.Release()
		}
	}()
	ticket, err := r.gate.Begin(ctx, c, e.spec, in)
	if err != nil {
		if errors.Is(err, ErrUnknown) || errors.Is(err, ErrInProgress) {
			return Result{State: Unknown}, err
		}
		return Result{State: Refused, Body: ticket.Result.Body}, err
	}
	if ticket.Replay {
		return ticket.Result, nil
	}
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	owned = false
	go func() {
		defer func() { l.Release(); <-e.slots }()
		out := outcome{result: Result{State: Unknown}}
		defer func() {
			if recover() != nil {
				out = outcome{Result{State: Unknown}, ErrUnknown}
			}
			done <- out
		}()
		out.err = lease.Do(ctx, l, func(ctx context.Context) error { var err error; out.result, err = e.handler(ctx, l, in); return err })
	}()
	var out outcome
	select {
	case out = <-done:
	case <-ctx.Done():
		out = outcome{Result{State: Unknown}, ctx.Err()}
	}
	switch out.result.State {
	case Applied:
		if out.err != nil || in.Write && out.result.Revision == "" {
			out.result = Result{State: Unknown}
			out.err = errors.Join(out.err, ErrUnknown)
		}
	case Failed, Cancelled:
	case None, Refused, Unknown:
		out.result.State = Unknown
		out.err = errors.Join(out.err, ErrUnknown)
	default:
		out.result = Result{State: Unknown}
		out.err = errors.Join(out.err, ErrUnknown)
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	if err := r.gate.Finish(finishCtx, ticket, out.result); err != nil {
		return Result{State: Unknown}, errors.Join(out.err, err)
	}
	return out.result, out.err
}
