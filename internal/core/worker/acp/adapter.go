// SPDX-License-Identifier: Apache-2.0
// Package acp drives installed ACP workers on the shared version 1 client.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	wire "github.com/thisisjun786/lina/kit/acp"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

var ErrThread = errors.New("ACP thread does not match current binding")
var ErrUnsupported = errors.New("ACP operation is unsupported")

var generations atomic.Uint64

type Config struct {
	Profile     Profile
	Environment []string
	Epoch       uint64
	Leaser      lease.Leaser
}
type Result struct {
	SessionID    string            `json:"session_id"`
	AgentVersion string            `json:"agent_version"`
	StopReason   string            `json:"stop_reason"`
	Output       string            `json:"output"`
	Unknown      []json.RawMessage `json:"unknown_updates,omitempty"`
}

// An adapter owns one session. The process lifetime is independent of individual
// control-call contexts; only Interrupt requests protocol cancellation.
type Adapter struct {
	cfg               Config
	control           sync.Mutex
	mu                sync.Mutex
	client            *wire.Client
	life              context.Context
	cancel            context.CancelFunc
	started, closed   bool
	thread            worker.Thread
	spec              worker.StartSpec
	state             worker.ThreadState
	version           string
	turn              uint64
	nextRequest       uint64
	requests          map[string]*pendingDecision
	cancelled         bool
	result            Result
	events            chan worker.Event
	turns             sync.WaitGroup
	tools             *toolServer
	pending           map[string]toolPending
	turnEnded         chan struct{}
	sessionFailures   atomic.Bool
	extensionFailure  *worker.Finish
	extensionObserved time.Time
	lastFinish        *worker.Finish
	quotaRows         []provider.QuotaRow
	nativeTools       map[string]bool
	nativeOverflow    bool
}

var _ worker.Adapter = (*Adapter)(nil)

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg, events: make(chan worker.Event, 256), state: worker.Unknown, pending: map[string]toolPending{}}
}
func (a *Adapter) Name() string { return a.cfg.Profile.Name }
func (a *Adapter) Capabilities() worker.CapabilitySet {
	caps := worker.CapabilitySet{worker.StreamProgress, worker.SignalCompletion, worker.WorkTools, worker.RequestApprovals, worker.AskQuestions}
	if a.cfg.Profile.RateLimitKey != "" || a.sessionFailures.Load() {
		caps = append(caps, worker.ReportQuota)
	}
	return caps
}
func (a *Adapter) Events() <-chan worker.Event { return a.events }

func (a *Adapter) Version(ctx context.Context) (string, error) {
	a.control.Lock()
	defer a.control.Unlock()
	a.mu.Lock()
	version, closed := a.version, a.closed
	a.mu.Unlock()
	if closed {
		return "", io.ErrClosedPipe
	}
	if version != "" {
		return version, nil
	}
	p := a.cfg.Profile
	if p.VersionCommand == "" {
		return "unknown", nil
	}
	if a.cfg.Leaser == nil {
		return "", lease.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	l, err := a.cfg.Leaser.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "ACP worker version"})
	if err != nil {
		return "", err
	}
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	var out boundedOutput
	err = owner.Run(ctx, l, proc.Command{Path: p.VersionCommand, Args: p.VersionArgs, Env: environment(a.cfg.Environment, p.Environment), Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return "", err
	}
	version = strings.TrimSpace(string(out.data))
	if version == "" {
		return "", errors.New("empty ACP worker version")
	}
	a.mu.Lock()
	a.version = version
	a.mu.Unlock()
	return version, nil
}
func (a *Adapter) Start(ctx context.Context, l lease.Lease, s worker.StartSpec) (worker.Thread, error) {
	a.control.Lock()
	defer a.control.Unlock()
	if err := ctx.Err(); err != nil {
		return worker.Thread{}, err
	}
	// Quota capability can only be known after extension negotiation.
	required := slices.DeleteFunc(slices.Clone(s.Required), func(c worker.Capability) bool { return c == worker.ReportQuota })
	if err := a.Capabilities().Require(required...); err != nil {
		return worker.Thread{}, err
	}
	if s.Model != "" || s.Effort != "" {
		return worker.Thread{}, ErrUnsupported
	}
	if !filepath.IsAbs(a.cfg.Profile.Command) || !filepath.IsAbs(s.Workdir) || s.TaskID == "" || s.AttemptID == "" || strings.TrimSpace(s.TaskPacket) == "" || a.cfg.Epoch == 0 {
		return worker.Thread{}, errors.New("invalid ACP start")
	}
	if err := lease.Do(ctx, l, func(context.Context) error { return nil }); err != nil {
		return worker.Thread{}, err
	}
	a.mu.Lock()
	if a.started || a.closed {
		a.mu.Unlock()
		return worker.Thread{}, errors.New("ACP adapter already started or closed")
	}
	a.started = true
	if len(s.Tools) == 0 {
		s.Tools = worker.WorkToolDeclarations()
	}
	a.spec = s
	a.life, a.cancel = context.WithCancel(ctx)
	a.mu.Unlock()
	c, err := wire.Start(a.life, l, proc.Command{Path: a.cfg.Profile.Command, Args: a.cfg.Profile.Args, Dir: s.Workdir, Env: environment(a.cfg.Environment, a.cfg.Profile.Environment)})
	if err != nil {
		return worker.Thread{}, err
	}
	a.mu.Lock()
	a.client = c
	a.mu.Unlock()
	handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	meta := map[string]json.RawMessage{"jetbrains": json.RawMessage(`{"air":{"version":1,"capabilities":["sessionFailure"]}}`)}
	initialized, err := c.InitializeWithMeta(handshake, wire.Implementation{Name: "lina", Version: "1"}, &meta)
	if err != nil {
		c.Close()
		return worker.Thread{}, err
	}
	a.sessionFailures.Store(advertisesFailures(initialized.Meta))
	if err := a.Capabilities().Require(s.Required...); err != nil {
		c.Close()
		return worker.Thread{}, err
	}
	server, err := a.startTools()
	if err != nil {
		c.Close()
		return worker.Thread{}, err
	}
	success := false
	defer func() {
		if !success {
			a.tools.close()
		}
	}()
	session, err := c.NewSession(handshake, wire.NewSessionRequest{Cwd: s.Workdir, McpServers: []wire.McpServer{server}})
	if err != nil {
		c.Close()
		var remote *wire.RPCError
		if errors.As(err, &remote) && remote.Code == -32000 {
			return worker.Thread{}, worker.ErrNeedsSignIn
		}
		return worker.Thread{}, err
	}
	a.mu.Lock()
	a.thread = worker.Thread{ID: string(session.SessionID), TaskID: s.TaskID, AttemptID: s.AttemptID, ProcessGeneration: generations.Add(1), BindingEpoch: a.cfg.Epoch}
	a.result.SessionID = a.thread.ID
	if initialized.AgentInfo != nil {
		a.result.AgentVersion = initialized.AgentInfo.Version
	}
	if a.version == "" {
		a.version = a.result.AgentVersion
	}
	if a.version == "" {
		a.version = "unknown"
	}
	notice := fmt.Sprintf("%s %s session started.", a.Name(), a.version)
	if !slices.Contains(a.cfg.Profile.VerifiedVersions, a.version) {
		verified := strings.Join(a.cfg.Profile.VerifiedVersions, ", ")
		if verified == "" {
			verified = "none"
		}
		notice = fmt.Sprintf("%s %s is unverified (verified: %s); the session is running.", a.Name(), a.version, verified)
	}
	// The first turn's readiness observation precedes every worker update. There
	// is exactly one such entry in the fresh event queue, so this cannot block.
	initial := a.thread
	initial.TurnID = fmt.Sprintf("acp:%d", a.turn+1)
	a.events <- worker.Event{Thread: initial, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "compatibility", Text: notice}}
	thread, err := a.beginLocked(ctx, s.TaskPacket)
	a.mu.Unlock()
	success = err == nil
	return thread, err
}
func (a *Adapter) beginLocked(ctx context.Context, text string) (worker.Thread, error) {
	a.turn++
	a.thread.TurnID = fmt.Sprintf("acp:%d", a.turn)
	a.turnEnded = make(chan struct{})
	a.state = worker.Active
	a.cancelled = false
	a.extensionFailure = nil
	a.lastFinish = nil
	a.result.Output = ""
	a.result.StopReason = ""
	th := a.thread
	result, err := a.client.StartPrompt(ctx, wire.PromptRequest{SessionID: wire.SessionID(th.ID), Prompt: []wire.ContentBlock{{Text: &wire.ContentBlockText{Text: text}}}})
	if err != nil {
		a.state = worker.Dead
		close(a.turnEnded)
		return th, err
	}
	a.turns.Add(1)
	go a.prompt(th, result)
	return th, nil
}
func (a *Adapter) matches(t worker.Thread) bool {
	return !a.closed && a.started && t.ID != "" && t == a.thread
}
func (a *Adapter) Input(ctx context.Context, t worker.Thread, in worker.Input) error {
	a.control.Lock()
	defer a.control.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.matches(t) {
		return ErrThread
	}
	if a.state != worker.Idle || strings.TrimSpace(in.Text) == "" {
		return fmt.Errorf("%w: ACP session is not idle", worker.ErrInputRefused)
	}
	_, err := a.beginLocked(ctx, in.Text)
	return err
}
func (a *Adapter) Interrupt(ctx context.Context, t worker.Thread, why worker.Reason) error {
	a.control.Lock()
	defer a.control.Unlock()
	switch why {
	case worker.UserCancel, worker.BudgetCap, worker.NodePolicyCancel, worker.StallRecovery:
	default:
		return ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	if !a.matches(t) {
		a.mu.Unlock()
		return ErrThread
	}
	if a.state != worker.Active && a.state != worker.Waiting {
		a.mu.Unlock()
		return fmt.Errorf("%w: ACP session is not active", worker.ErrInputRefused)
	}
	c := a.client
	a.cancelled = true
	a.requests = nil
	a.state = worker.Active
	a.mu.Unlock()
	return c.Cancel(ctx, wire.SessionID(t.ID))
}
func (a *Adapter) Snapshot(ctx context.Context, t worker.Thread) (worker.ThreadSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return worker.ThreadSnapshot{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Snapshot discovers the current turn within the same process binding.
	// Input and Interrupt still require the exact current turn id.
	t.TurnID = a.thread.TurnID
	if !a.matches(t) {
		return worker.ThreadSnapshot{}, ErrThread
	}
	var finish *worker.Finish
	if a.lastFinish != nil {
		copy := *a.lastFinish
		finish = &copy
	}
	return worker.ThreadSnapshot{Finish: finish, EffectsKnown: finish != nil && finish.Failure != worker.ConnectionFailure && a.effectsKnownLocked(), Thread: a.thread, State: a.state, Worker: a.Name(), Version: a.version, Workdir: a.spec.Workdir, Capabilities: a.Capabilities(), Output: a.result.Output, Pending: a.pendingLocked()}, nil
}
func (a *Adapter) Result() Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := a.result
	r.Unknown = append([]json.RawMessage(nil), r.Unknown...)
	return r
}
func (a *Adapter) Close(context.Context) error {
	a.control.Lock()
	defer a.control.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	c, cancel := a.client, a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c != nil {
		c.Close()
	}
	if a.tools != nil {
		a.tools.close()
	}
	a.turns.Wait()
	close(a.events)
	return nil
}

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 4096 {
		return 0, errors.New("ACP version output too large")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
