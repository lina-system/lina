// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/kit/provider"
)

type execution struct {
	inFlight int
	cancel   context.CancelCauseFunc
}
type Service struct {
	masker   masker
	root     *os.Root
	cfg      Config
	mu       sync.Mutex
	live     map[string]execution
	failures map[string]error
	changed  chan struct{}
	closed   bool
	wg       sync.WaitGroup
}

func New(cfg Config) (*Service, error) {
	if cfg.DB == nil || cfg.Assets == nil || cfg.Stream == nil || cfg.Authorize == nil || cfg.Epoch == 0 {
		return nil, ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	copied := make(map[string]Tool, len(cfg.Tools))
	for name, t := range cfg.Tools {
		var d struct{ Name string }
		if json.Unmarshal(t.Declaration, &d) != nil || d.Name != name {
			return nil, ErrInvalid
		}
		t.Declaration = append(provider.ToolDecl(nil), t.Declaration...)
		copied[name] = t
	}
	cfg.Tools = copied
	root, err := openWork(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	return &Service{root: root, cfg: cfg, live: map[string]execution{}, failures: map[string]error{}, changed: make(chan struct{})}, nil
}
func (s *Service) signal() { close(s.changed); s.changed = make(chan struct{}) }
func valid(o Origin, b Brief) bool {
	if b.Mock != nil && (b.Result != "mock" || b.Mock.ID == "" || b.Mock.Revision < 1) {
		return false
	}
	if b.Result == "mock" && o.Job != "" {
		return false
	}
	if o.Person == "" || b.Goal == "" || b.Scope == "" || b.NotTouch == "" || b.Done == "" || b.Result == "" || len(b.Checks) == 0 {
		return false
	}
	if o.Job == "" && (o.Agent == "" || o.Conversation == "" || o.Turn == "") {
		return false
	}
	if o.Job != "" && (o.Agent != "" || o.Conversation != "" || o.Turn != "") {
		return false
	}
	for _, c := range b.Checks {
		if c.Name == "" || (c.Kind != CitationsResolve && c.Kind != ToolCalled && c.Kind != Claimed) {
			return false
		}
	}
	for _, r := range b.Refs {
		if r.ID == "" || r.Revision < 1 {
			return false
		}
	}
	return true
}
func capacity(rows []Record, o Origin) bool {
	person, agent := 0, 0
	for _, r := range rows {
		if r.State == Working && r.Origin.Person == o.Person {
			person++
			if o.Agent != "" && r.Origin.Agent == o.Agent {
				agent++
			}
		}
	}
	return person < 8 && agent < 4
}

// Start commits and reserves a slot, without invoking a model. The tool caller
// must publish/consume the returned id before Release; this explicit barrier
// avoids a goroutine race between returning the receipt and the first call.
// The caller's turn context is used only for admission, never for execution.
func (s *Service) Start(ctx context.Context, o Origin, b Brief) (string, State, error) {
	return s.start(ctx, o, b, nil)
}

// The native origin and call identity are bound outside the model arguments.
// Epoch is excluded: a lost receipt is still the same call after Core restarts.
func startIdentity(o Origin, call string) string {
	o.Epoch = 0
	raw, _ := json.Marshal(struct {
		Origin Origin
		Call   string
	}{o, call})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// replayStart reads an already committed receipt, without admitting new work.
// The native origin/call binding is trusted; budget and account holds may have
// changed since admission and cannot invalidate a lost receipt.
func (s *Service) replayStart(ctx context.Context, key, digest string) (string, State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", "", ErrClosed
	}
	rows, err := s.records(ctx)
	if err != nil {
		return "", "", err
	}
	for _, r := range rows {
		if r.StartKey != key {
			continue
		}
		if r.StartDigest != digest {
			return "", "", ErrInvalid
		}
		return r.ID, r.StartState, nil
	}
	return "", "", nil
}

func (s *Service) start(ctx context.Context, o Origin, b Brief, promotion *budget.Handoff) (string, State, error) {
	return s.startWithCall(ctx, o, b, promotion, "")
}

func (s *Service) startWithCall(ctx context.Context, o Origin, b Brief, promotion *budget.Handoff, call string) (string, State, error) {
	o.Epoch = s.cfg.Epoch
	if !valid(o, b) {
		return "", "", ErrInvalid
	}
	// Round-trip the brief to detach caller-owned slices.
	data, _ := json.Marshal(b)
	var detached Brief
	_ = json.Unmarshal(data, &detached)
	b = detached
	var key, digest string
	if call != "" {
		key = startIdentity(o, call)
		sum := sha256.Sum256(data)
		digest = hex.EncodeToString(sum[:])
		if id, state, err := s.replayStart(ctx, key, digest); err != nil || id != "" {
			return id, state, err
		}
	}
	if promotion != nil {
		if id, state, err := s.replayPromotion(ctx, o, b, promotion); err != nil || id != "" {
			return id, state, err
		}
	}
	if err := s.cfg.Authorize(ctx, o, b); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", "", ErrClosed
	}
	rows, err := s.records(ctx)
	if err != nil {
		return "", "", err
	}
	// Another caller may have committed while authorization was in progress.
	if call != "" {
		for _, prior := range rows {
			if prior.StartKey != key {
				continue
			}
			if prior.StartDigest != digest {
				return "", "", ErrInvalid
			}
			return prior.ID, prior.StartState, nil
		}
	}
	if promotion != nil {
		if id, state, err := findPromotion(rows, o, b, promotion); err != nil || id != "" {
			return id, state, err
		}
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", "", err
	}
	now := s.cfg.Now().UnixNano()
	r := Record{ID: hex.EncodeToString(id), Origin: o, Brief: b, State: Waiting, Created: now, LastEvent: now, StartKey: key, StartDigest: digest}
	r.Promotion = promotion
	if promotion != nil {
		r.PromotionDigest = promotionDigest(b, promotion)
	}
	if promotion != nil && len(promotion.Effects) > 0 {
		r.EffectsQuestion = &EffectQuestion{RunID: r.ID, Turn: o.Turn, State: "awaiting_person", Effects: promotion.Effects}
	}
	r.Scratch = filepath.Join("runs", r.ID, "scratch")
	if err := s.prepareFiles(r.ID); err != nil {
		return "", "", err
	}
	if err := s.transcript(r.ID, "brief", r.Brief); err != nil {
		return "", "", err
	}
	seen := map[string]bool{}
	for _, name := range b.Allow {
		t, ok := s.cfg.Tools[name]
		if ok && !seen[name] && (t.Kind == Read || t.Kind == Scratch) && t.Authorize != nil && t.Handler.Validate != nil && t.Handler.Execute != nil {
			r.Scope = append(r.Scope, name)
			seen[name] = true
		}
	}
	if capacity(rows, o) {
		r.State = Working
	}
	r.StartState = r.State
	if err := s.save(ctx, r, true); err != nil {
		return "", "", err
	}
	return r.ID, r.State, nil
}

// Release is also an explicit wake after a budget/account hold. It does not
// restart terminal runs or consume the single interruption retry.
func (s *Service) Release(ctx context.Context, id string) error {
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
		return nil
	}
	if r.State == NeedsInput {
		return ErrState
	}
	delete(s.failures, id)
	r.Released = true
	r.Reason = ""
	if err = s.save(ctx, r, false); err != nil {
		return err
	}
	return s.schedule(ctx)
}
func (s *Service) schedule(ctx context.Context) error {
	if s.closed {
		return nil
	}
	rows, err := s.records(ctx)
	if err != nil {
		return err
	}
	for i := range rows {
		r := rows[i]
		if s.failures[r.ID] != nil {
			continue
		}
		if !r.Released || r.Reason != "" || r.State == Landed || r.State == NeedsInput {
			continue
		}
		if _, ok := s.live[r.ID]; ok {
			continue
		}
		if r.State == Waiting && r.PendingOutcome == "" {
			if !capacity(rows, r.Origin) {
				continue
			}
			r.State = Working
			rows[i] = r
		}
		r.LastEvent = s.cfg.Now().UnixNano()
		if err := s.save(ctx, r, false); err != nil {
			return err
		}
		runCtx, cancel := context.WithCancelCause(context.Background())
		s.live[r.ID] = execution{cancel: cancel}
		s.wg.Add(1)
		go func(id string) {
			defer s.wg.Done()
			err := s.run(runCtx, id)
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.live, id)
			if err != nil {
				s.failures[id] = err
			}
			if e := s.schedule(context.Background()); e != nil {
				s.failures[id] = errors.Join(err, e)
			}
			s.signal()
		}(r.ID)
	}
	return nil
}
func (s *Service) Wait(ctx context.Context, id string) error {
	for {
		s.mu.Lock()
		r, err := s.read(ctx, id)
		changed := s.changed
		if e := s.failures[id]; e != nil {
			err = e
		}
		closed := s.closed
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if r.State == Landed || r.State == NeedsInput || r.State == Waiting && r.Reason != "" {
			return nil
		}
		if closed {
			return ErrClosed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closed = true
	for _, e := range s.live {
		e.cancel(ErrClosed)
	}
	s.signal()
	s.mu.Unlock()
	s.wg.Wait()
	return s.root.Close()
}
func (s *Service) Follow(ctx context.Context, id, note string) error {
	return s.FollowOnce(ctx, id, "", note)
}

// FollowOnce retains the request identity with the queued note, so reconnects
// cannot apply the same steering twice. Empty keys preserve internal callers.
func (s *Service) FollowOnce(ctx context.Context, id, key, note string) error {
	return s.FollowScoped(ctx, id, key, note, nil)
}

// FollowScoped retains source restrictions with the run, including after the
// note is consumed into a checkpoint. Only trusted Core callers supply source.
func (s *Service) FollowScoped(ctx context.Context, id, key, note string, source json.RawMessage) error {
	if note == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if key != "" {
		for _, seen := range r.FollowKeys {
			if seen == key {
				return nil
			}
		}
	}
	if r.State == Landed || r.Brief.Result == "mock" && mockPublicationReady(r.MockState) {
		return ErrState
	}
	if key != "" {
		r.FollowKeys = append(r.FollowKeys, key)
	}
	if len(source) > 0 {
		if !json.Valid(source) || len(source) > 16384 {
			return ErrInvalid
		}
		if s.cfg.AuthorizeFollowUp == nil {
			return ErrScope
		}
		if err := s.cfg.AuthorizeFollowUp(ctx, r, source); err != nil {
			return err
		}
		found := false
		for _, prior := range r.FollowSources {
			if string(prior) == string(source) {
				found = true
				break
			}
		}
		if !found {
			r.FollowSources = append(r.FollowSources, append(json.RawMessage(nil), source...))
		}
	}
	r.FollowUps = append(r.FollowUps, note)
	if r.State == NeedsInput {
		r.Noticed = false
		r.State = Waiting
		r.Reason = ""
	}
	if err = s.save(ctx, r, false); err != nil {
		return err
	}
	return s.schedule(ctx)
}
func (s *Service) Stop(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if r.State == Landed {
		return nil
	}
	if e, ok := s.live[id]; ok {
		e.cancel(context.Canceled)
	}
	if err = s.land(ctx, &r, Cancelled); err != nil {
		return err
	}
	return s.schedule(ctx)
}

// Recover runs before admitting work in the new Core epoch. Waiting/account
// holds and cancelled records are unchanged; only interrupted working runs
// consume the one persisted retry and require Release. Already released queued
// runs are scheduled after the recovery writes finish.
func (s *Service) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.live) != 0 {
		return ErrState
	}
	rows, err := s.records(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State == Landed {
			continue
		}
		if err := s.prepareFiles(r.ID); err != nil {
			return err
		}
		r.Origin.Epoch = s.cfg.Epoch
		// Admission already published this handle atomically to its source turn.
		// A crash before the caller consumes it must not strand unstarted work
		// or consume the interruption retry reserved for actual execution.
		if r.Promotion != nil && !r.Released && r.Retries == 0 && r.PendingOutcome == "" && r.State != NeedsInput {
			r.Released = true
			if err := s.save(ctx, r, false); err != nil {
				return err
			}
			continue
		}
		if r.PendingOutcome != "" {
			r.State = Waiting
			r.Released = true
			r.Reason = ""
			if err := s.save(ctx, r, false); err != nil {
				return err
			}
			continue
		}
		// An explicit admission still awaiting its caller receipt has never
		// executed. Core reconciles that receipt before releasing it.
		if r.StartKey != "" && r.Promotion == nil && !r.Released && r.Retries == 0 {
			if err := s.save(ctx, r, false); err != nil {
				return err
			}
			continue
		}
		if r.State != Working {
			if err := s.save(ctx, r, false); err != nil {
				return err
			}
			continue
		}
		if r.Retries == 0 {
			r.Retries++
			r.State = Waiting
			r.Released = false
			r.Reason = ""
			err = s.save(ctx, r, false)
		} else {
			r.Reason = "interrupted"
			err = s.land(ctx, &r, failureOutcome(r))
		}
		if err != nil {
			return err
		}
	}
	return s.schedule(ctx)
}

// CheckProgress is called by Core's liveness timer. Age is never a deadline:
// only a whole stale interval without model/tool activity interrupts a run.
func (s *Service) CheckProgress(ctx context.Context, now time.Time, stale time.Duration) error {
	if stale <= 0 {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.records(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.State != Working {
			continue
		}
		if r.CheckedInAt == 0 && now.Sub(time.Unix(0, r.Created)) >= 15*time.Minute {
			r.CheckedInAt = now.UnixNano()
			if err := s.save(ctx, r, false); err != nil {
				return err
			}
		}
		if now.Sub(time.Unix(0, r.LastEvent)) >= stale {
			if e, ok := s.live[r.ID]; ok && e.inFlight == 0 {
				e.cancel(ErrStalled)
			}
		}
	}
	return nil
}
func failureOutcome(r Record) Outcome {
	if r.Artifact.Body != "" || len(r.Calls) > 0 {
		return Partial
	}
	return Failed
}
func item(text string) provider.Item {
	b, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": text})
	return b
}

// Changed returns the current notification generation. Capture it before
// reading projections, then wait on it to avoid missing a committed change.
func (s *Service) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}
