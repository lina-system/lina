// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

const instructions = `Execute only the self-contained background brief and its named references. Do not start work or other runs. Return one JSON object with body (the useful result), done (your claim), missing (unfinished work), and citations (revision-pinned passage anchors). Core checks the claims and citations.`

func (s *Service) update(ctx context.Context, id string, fn func(*Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if r.State == Landed {
		return ErrState
	}
	if err = fn(&r); err != nil {
		return err
	}
	return s.save(ctx, r, false)
}

// An adapter owns its individual call deadline. Until that deadline expires,
// an in-flight request/tool is activity even when it emits no streaming event.
func (s *Service) activity(id string, delta int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.live[id]; ok {
		e.inFlight += delta
		s.live[id] = e
	}
}
func (s *Service) touch(ctx context.Context, id string) error {
	return s.update(ctx, id, func(r *Record) error { r.LastEvent = s.cfg.Now().UnixNano(); return nil })
}
func (s *Service) run(ctx context.Context, id string) error {
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	var scopeFailed atomic.Bool
	cfg := loop.Config{Request: provider.Request{Model: s.cfg.Model, Purpose: provider.Background, Instructions: instructions}, Tools: map[string]loop.Tool{}}
	if r.Origin.Job != "" {
		cfg.Request.Purpose = provider.BackgroundJob
	}
	cfg.Request.Items = r.Checkpoint
	if len(cfg.Request.Items) == 0 {
		b, _ := json.Marshal(r.Brief)
		cfg.Request.Items = []provider.Item{item(string(b))}
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	drained := 0
	cfg.Inputs = inputQueue{drain: func() []provider.Item {
		current, err := s.read(runCtx, id)
		if err != nil {
			cancel(err)
			return nil
		}
		var notes []provider.Item
		for _, note := range current.FollowUps[drained:] {
			notes = append(notes, item(note))
		}
		drained = len(current.FollowUps)
		return notes
	}}
	for _, name := range r.Scope {
		tool, ok := s.cfg.Tools[name]
		if !ok || tool.Authorize == nil || (tool.Kind != Read && tool.Kind != Scratch) {
			continue
		}
		cfg.Request.Tools = append(cfg.Request.Tools, tool.Declaration)
		cfg.Tools[name] = loop.Tool{
			Validate: func(c provider.ToolCall) (string, error) {
				target, err := tool.Handler.Validate(c)
				if tool.Kind == Read && target != "" {
					return "", ErrScope
				}
				return target, err
			},
			Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
				current, err := s.read(ctx, id)
				if err == nil && current.State != Working {
					err = ErrScope
				}
				if err == nil {
					err = tool.Authorize(ctx, current, c)
				}
				if err != nil {
					scopeFailed.Store(true)
					if errors.Is(err, ErrNeedsInput) {
						cancel(ErrNeedsInput)
					}
					return loop.Result{ID: c.ID, Code: loop.Denied}
				}
				if err := s.touch(ctx, id); err != nil {
					return loop.Result{ID: c.ID, Code: loop.Aborted}
				}
				s.activity(id, 1)
				result := tool.Handler.Execute(ctx, c)
				s.activity(id, -1)
				if err := s.update(context.WithoutCancel(ctx), id, func(r *Record) error {
					r.LastEvent = s.cfg.Now().UnixNano()
					if result.Code == loop.OK {
						r.Calls = append(r.Calls, c.Name)
					}
					return nil
				}); err != nil {
					cancel(err)
				}
				return result
			},
		}
	}
	// Transform runs after the preceding tool results have joined model context.
	// Commit that checkpoint before authorizing/sending the next request. Steering
	// is appended and removed from its durable queue in the same transaction.
	cfg.Transform = func(ctx context.Context, req provider.Request) (provider.Request, error) {
		if err := s.update(ctx, id, func(r *Record) error {
			req.Items = append([]provider.Item(nil), req.Items...)
			r.FollowUps = r.FollowUps[drained:]
			drained = 0
			r.Checkpoint = req.Items
			return nil
		}); err != nil {
			return req, err
		}
		current, err := s.read(ctx, id)
		if err != nil {
			return req, err
		}
		if err := s.cfg.Authorize(ctx, current.Origin, current.Brief); err != nil {
			return req, err
		}
		if s.cfg.Prepare != nil {
			items, err := s.cfg.Prepare(ctx, current, req.Items)
			if err != nil {
				return req, err
			}
			req.Items = items
		}
		return req, nil
	}
	cfg.Stream = func(ctx context.Context, req provider.Request) (provider.Stream, error) {
		s.activity(id, 1)
		st, err := s.cfg.Stream(ctx, req)
		if err != nil {
			s.activity(id, -1)
			return nil, err
		}
		if st == nil {
			s.activity(id, -1)
			return nil, loop.ErrResponse
		}
		return &trackedStream{Stream: st, finished: func(meta provider.Meta) error {
			s.activity(id, -1)
			return s.update(context.WithoutCancel(ctx), id, func(r *Record) error { r.Usage = append(r.Usage, meta.Usage); return nil })
		}}, nil
	}
	cfg.Observe = func(ctx context.Context, _ provider.Event) error { return s.touch(ctx, id) }
	// Completed assistant items are kept even if the following stream fails.
	cfg.Journal.Item = func(ctx context.Context, _ int, _ int64, b provider.Item) error {
		return s.update(ctx, id, func(r *Record) error {
			if err := s.transcript(id, "item", b); err != nil {
				return err
			}
			if a, ok := artifact([]provider.Item{b}); ok {
				r.Artifact = a
			}
			return nil
		})
	}
	cfg.Journal.Result = func(ctx context.Context, _ int, _ int, result loop.Result) error {
		return s.update(ctx, id, func(r *Record) error { return s.transcript(id, "tool_result", result) })
	}
	cfg.Journal.Partial = func(ctx context.Context, _ int, _ int64, text string) error {
		return s.update(ctx, id, func(r *Record) error { return s.transcript(id, "partial", text) })
	}
	out, runErr := loop.Run(runCtx, cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	} // retain working for recovery under the next epoch
	r, err = s.read(context.Background(), id)
	if err != nil {
		return err
	}
	if r.State == Landed {
		return nil
	} // cancellation always wins over late completion
	if a, ok := artifact(out.Items); ok {
		r.Artifact = a
	}
	if runErr == nil {
		r.Checkpoint = out.Items
		if len(r.FollowUps) > 0 { // input arrived after the final model snapshot
			r.State = Waiting
			return s.save(context.Background(), r, false)
		}
		outcome := Complete
		if !r.Artifact.Done || r.Artifact.Body == "" || scopeFailed.Load() {
			outcome = failureOutcome(r)
		}
		return s.land(context.Background(), &r, outcome)
	}
	cause := context.Cause(runCtx)
	if errors.Is(cause, ErrNeedsInput) {
		r.NoticeVersion++
		r.NoticeAt = s.cfg.Now().UnixNano()
		r.State = NeedsInput
		r.Reason = "needs_input"
		return s.save(context.Background(), r, false)
	}
	var failure *provider.Failure
	if errors.Is(runErr, ErrBudget) || errors.As(runErr, &failure) && (failure.Kind == provider.UsageCapped || failure.Kind == provider.NeedsSignIn || failure.Kind == provider.NotEligible) {
		r.State = Waiting
		r.Reason = "budget"
		if failure != nil {
			r.Reason = string(failure.Kind)
		}
		return s.save(context.Background(), r, false)
	}
	if r.Retries == 0 {
		r.Retries++
		r.State = Waiting
		r.Reason = ""
		return s.save(context.Background(), r, false)
	}
	r.Reason = "interrupted"
	return s.land(context.Background(), &r, failureOutcome(r))
}

type trackedStream struct {
	provider.Stream
	finished func(provider.Meta) error
}

func (s *trackedStream) Close() error {
	err := s.Stream.Close()
	return errors.Join(err, s.finished(s.Stream.Meta()))
}
func artifact(items []provider.Item) (Artifact, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		var message struct {
			Type, Role string
			Content    []struct{ Type, Text string }
		}
		if json.Unmarshal(items[i], &message) != nil || message.Type != "message" || message.Role != "assistant" {
			continue
		}
		for _, c := range message.Content {
			if c.Type != "output_text" {
				continue
			}
			var a Artifact
			if json.Unmarshal([]byte(c.Text), &a) == nil && a.Body != "" {
				return a, true
			}
		}
	}
	return Artifact{}, false
}
