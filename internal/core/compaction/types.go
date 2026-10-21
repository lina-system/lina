// SPDX-License-Identifier: Apache-2.0

// Package compaction publishes derived summary trees through canon's writer.
package compaction

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid       = errors.New("invalid_summary")
	ErrCoverage      = errors.New("invalid_summary_coverage")
	ErrStaleFrontier = errors.New("stale_summary_frontier")
	ErrStaleEpoch    = errors.New("stale_summary_epoch")
	ErrUnavailable   = errors.New("summary_unavailable")
)

// Frontier fences both ledger appends and competing publications/rebuilds.
// The revocation epoch is separate from canon's process ownership epoch.
type Frontier struct {
	Conversation string
	Root         string
	Revision     int64
	LastEvent    string
	Events       int
	Epoch        uint64
}

// Coverage assigns a raw event to exactly one node. Empty Reason means input;
// replay, scaffold, summary_echo and retracted record deliberate exclusions.
type Coverage struct{ EventID, Reason string }

type Output struct {
	Model, PromptVersion string
	BuiltAt              time.Time
	Text, Dropped        string
}

type Proposal struct {
	Expected Frontier
	ID       string
	Children []string
	Cover    []Coverage
	Verbatim []string
	Output   Output
}

type Node struct {
	ID, Conversation string
	Level            int
	Epoch            uint64
	Revision         int64
	Status           string
	StartAt, EndAt   time.Time
	Output           Output
	Children         []string
	Cover            []Coverage
}

// Segment contains only direct visible event locators and immediate children.
// The consuming summarizer resolves authorized, masked bodies (including exact
// material revisions), envelopes them as data, and makes the auxiliary request.
// It must return an error instead of a partial or head-only fallback summary.
type Segment struct {
	NodeID   string
	Events   []string
	Children []Node
}

type Summarizer func(context.Context, Segment) (Output, error)
