// SPDX-License-Identifier: Apache-2.0

// Package runs owns durable background sub-agent runs on the kit loop.
package runs

import (
	"context"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/materials"
	"github.com/thisisjun786/lina/kit/provider"
)

var (
	ErrInvalid    = errors.New("invalid_run")
	ErrBudget     = errors.New("budget")
	ErrClosed     = errors.New("runs_closed")
	ErrState      = errors.New("run_state")
	ErrStalled    = errors.New("run_stalled")
	ErrNeedsInput = errors.New("needs_input")
	ErrScope      = errors.New("run_scope")
)

type State string

const (
	Waiting    State = "waiting"
	Working    State = "working"
	NeedsInput State = "needs_input"
	Landed     State = "landed"
)

type Outcome string

const (
	Complete  Outcome = "complete"
	Partial   Outcome = "partial"
	Failed    Outcome = "failed"
	Cancelled Outcome = "cancelled"
)

type CheckKind string

const (
	CitationsResolve CheckKind = "citations_resolve"
	ToolCalled       CheckKind = "tool_called"
	Claimed          CheckKind = "claimed"
)

type Check struct {
	Name string
	Kind CheckKind
}
type CheckResult struct {
	Check    Check
	Passed   bool
	Verified bool
}
type Ref struct {
	ID       string
	Revision int64
}
type Origin struct {
	Person, Agent, Conversation, Turn, Job string
	Epoch                                  uint64
}
type Brief struct {
	Goal, Scope, NotTouch, Done string
	Checks                      []Check
	Result                      string
	Allow                       []string
	Refs                        []Ref
}
type Artifact struct {
	Body      string             `json:"body"`
	Done      bool               `json:"done"`
	Missing   []string           `json:"missing,omitempty"`
	Citations []materials.Anchor `json:"citations,omitempty"`
}
type Record struct {
	Promotion                   *budget.Handoff
	EffectsQuestion             *EffectQuestion
	CheckedInAt                 int64
	Scratch                     string // relative to Config.WorkDir; only this directory is writable by run tools
	ID                          string
	Origin                      Origin
	Brief                       Brief
	Scope                       []string
	State                       State
	Outcome                     Outcome
	Released                    bool
	Retries                     int
	Checkpoint                  []provider.Item
	FollowUps                   []string
	Calls                       []string
	Usage                       []provider.Usage // unknown remains unknown; rows are attributed by this run's Origin
	Artifact                    Artifact
	Material                    Ref
	Citations                   []materials.Citation
	Checks                      []CheckResult
	Reason                      string
	Created, Updated, LastEvent int64
	Noticed                     bool
}
type ToolKind uint8

const (
	Read ToolKind = iota + 1
	Scratch
)

// Tool is trusted Core configuration, never metadata supplied by a model or a
// plugin. Only read and scratch adapters are eligible. Authorize rechecks the
// spawning authority, epoch, owner, named reference/revision and target at each
// dispatch. An approval-requiring adapter must not be registered here.
type Tool struct {
	Declaration provider.ToolDecl
	Kind        ToolKind
	Handler     loop.Tool
	Authorize   func(context.Context, Record, provider.ToolCall) error
}
type Canon interface {
	Read(context.Context, func(*canon.Tx) error) error
	Write(context.Context, func(*canon.Tx) error) error
}
type Config struct {
	Epoch   uint64 // current Core epoch, supplied by the composition root
	WorkDir string // private Core work area, not a conversation workspace
	DB      Canon
	Assets  *assets.Store
	Stream  loop.StreamFunc
	Tools   map[string]Tool
	// Authorize validates origin and scope and checks the person's shared budget.
	// It runs at start and before every model request; ErrBudget parks a run.
	Authorize func(context.Context, Origin, Brief) error
	Model     provider.ModelRef
	// Prepare supplies only the named projections and may condense old tool
	// outputs. It cannot change tools, purpose or background instructions.
	Prepare func(context.Context, Record, []provider.Item) ([]provider.Item, error)
	Now     func() time.Time
}

// Notice contains only landing metadata. The artifact enters a conversation
// only when its material is explicitly read, never as a notice body.
type Notice struct {
	State     State
	ID        string
	Origin    Origin
	Outcome   Outcome
	Result    string
	Material  Ref
	Citations []materials.Citation
	Checks    []CheckResult
	Reason    string
}
