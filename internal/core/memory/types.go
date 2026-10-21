// SPDX-License-Identifier: Apache-2.0

// Package memory owns memory items and their scoped views. Core supplies trusted
// evidence resolution inside the same fenced transaction as each write.
package memory

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
)

var (
	ErrInvalid        = errors.New("invalid_memory_item")
	ErrEvidence       = errors.New("invalid_memory_evidence")
	ErrPersonEvidence = errors.New("person_memory_requires_person_events")
	ErrScope          = errors.New("memory_scope_refused")
	ErrSupersedes     = errors.New("invalid_memory_supersession")
)

type Kind string

const (
	Fact               Kind = "fact"
	Preference         Kind = "preference"
	Commitment         Kind = "commitment"
	Decision           Kind = "decision"
	Correction         Kind = "correction"
	SharedHistory      Kind = "shared_history"
	Relationship       Kind = "relationship"
	EmotionObservation Kind = "emotion_observation"
)

type SubjectKind string

const (
	Person     SubjectKind = "person"
	ThirdParty SubjectKind = "third_party"
	World      SubjectKind = "world"
)

type Subject struct {
	Kind SubjectKind
	ID   string
}
type Holder string

const All Holder = "all"

type Scope string

const (
	Local       Scope = "local"
	Private     Scope = "private"
	NeverLeaves Scope = "never-leaves"
)

type Basis string

const (
	Stated     Basis = "stated"
	Derived    Basis = "derived"
	Imported   Basis = "imported"
	Automation Basis = "automation"
)

type State string

const (
	Active    State = "active"
	Retracted State = "retracted"
	Trashed   State = "trashed"
)

type EvidenceKind string

const (
	Event      EvidenceKind = "event"
	Material   EvidenceKind = "material"
	WorkResult EvidenceKind = "work_result"
	MemoryItem EvidenceKind = "item"
	External   EvidenceKind = "external"
)

type Precision string

const (
	Exact       Precision = "event"
	Session     Precision = "session"
	NoPrecision Precision = "none"
)

// Evidence's metadata is filled from canon, never trusted from a proposed item.
// Quote is removed on recall when the evidence belongs to another agent.
type Evidence struct {
	Kind        EvidenceKind
	ID          string
	Revision    int64
	Precision   Precision
	SourceAgent string
	OccurredAt  int64
	Quote       string
}
type Item struct {
	ID                        string
	Kind                      Kind
	Content, Gloss            string
	Holder                    Holder
	Person                    string
	Subject                   Subject
	SourcePerson, SourceAgent string
	Evidence                  []Evidence
	ValidFrom, ValidUntil     int64
	Basis                     Basis
	Scope                     Scope
	Revision                  int64
	State                     State
	Standing                  bool
	Supersedes                string
	Sensitivity               []string
}

// Origin is read by the flow from the canonical source, including its actual
// scope, current availability and verification verdict. Unsupported sources
// must return ErrEvidence; a model cannot provide this resolver.
type Origin struct {
	Person, Agent string
	Speaker       guard.SpeakerKind
	Quote         string
	OccurredAt    int64
	Scope         Scope
	Sensitivity   []string
	Verified      bool
	PlanEnded     bool
}
type ResolveEvidence func(context.Context, *canon.Tx, Evidence) (Origin, error)

// Write is submitted by Core's Moirai write path. Agent and Person are bound by
// the authenticated flow, not supplied as tool arguments. A holder override
// requires the caller to attest the person's explicit scope instruction.
type Write struct {
	Agent, Person   string
	Item            Item
	ExplicitHolder  bool
	AgentPreference bool
}

// RecallScope is the only entry to candidate selection. Holder and person
// filtering happens in SQL before text matching, ranking, or a result limit.
type RecallScope struct {
	Agent, Person string
	Projection    bool
}

func (s RecallScope) Holders() []Holder { return []Holder{Holder(s.Agent), All} }

type Query struct {
	Text       string
	At         int64
	Limit      int
	History    bool
	PersonOnly bool
}
type Hit struct {
	Item
	Older bool
}
