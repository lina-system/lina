// SPDX-License-Identifier: Apache-2.0
// Package work owns the append-only delegated-task ledger and its projections.
package work

import (
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/result"
	"time"
)

var (
	ErrInvalid   = errors.New("invalid work record")
	ErrState     = errors.New("work state conflict")
	ErrActiveRun = errors.New("task already has an active run")
	ErrNotFound  = errors.New("work task not found")
)

type Task struct {
	WorkKind        string     `json:"work_kind,omitempty"`
	Destination     string     `json:"destination,omitempty"`
	ExternalControl bool       `json:"external_control,omitempty"`
	Workspace       *Workspace `json:"workspace,omitempty"`
	PersonID        string     `json:"person_id,omitempty"`
	GoalID          string     `json:"goal_id,omitempty"`
	ID              string     `json:"id"`
	AgentID         string     `json:"agent_id"`
	ConversationID  string     `json:"conversation_id"`
	Title           string     `json:"title"`
	Done            string     `json:"done"`
	ArtifactRoots   []string   `json:"artifact_roots"`
	RequiresApply   bool       `json:"requires_apply"`
}

type Run struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Generation int    `json:"generation"`
	Worker     string `json:"worker"`
	ThreadID   string `json:"thread_id"`
	TurnID     string `json:"turn_id"`
	NodeID     string `json:"node_id"`
}

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  *int64 `json:"bytes,omitempty"`
}
type TurnRef struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	TurnStatus string `json:"turnStatus"`
}
type Provenance struct {
	ArtifactID string    `json:"artifact_id"`
	TaskID     string    `json:"task_id"`
	NodeID     string    `json:"node_id"`
	Path       string    `json:"path"`
	SHA256     string    `json:"sha256"`
	ObservedAt time.Time `json:"observed_at"`
}

// Receipt retains the pinned CRW record names. LINA adds only the schema version
// and provenance. Worker criteria are claims, never Core acceptance verdicts.
type Receipt struct {
	SchemaVersion       int          `json:"schema_version"`
	EventID             string       `json:"eventId"`
	RelationshipID      string       `json:"relationshipId"`
	ExecutionGeneration int          `json:"executionGeneration"`
	Attempt             int          `json:"attempt"`
	RevisionHash        string       `json:"revisionHash"`
	Outcome             string       `json:"outcome"`
	Producer            string       `json:"producer"`
	TurnRef             TurnRef      `json:"turnRef"`
	EmittedAt           time.Time    `json:"emittedAt"`
	Manifest            []Artifact   `json:"manifest"`
	Provenance          []Provenance `json:"provenance"`
}

type Stage struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Source  string `json:"source"`
}
type Wait struct {
	Round, Question string
	Held            bool
	Value           string
}
type GoalDecision struct {
	Round    string
	Decision QuestionDecision
}
type Card struct {
	Completion     *result.Completion `json:"completion,omitempty"`
	Head           string             `json:"head,omitempty"`
	CompletionHead string             `json:"completion_head,omitempty"`
	FileSnapshot   *FileSnapshot      `json:"file_snapshot,omitempty"`
	FileEvidence   *FileEvidence      `json:"file_evidence,omitempty"`
	FileCopies     []FileCopy         `json:"file_copies,omitempty"`
	Materials      []Material         `json:"materials,omitempty"`
	ResultHead     string             `json:"result_head,omitempty"`
	Revision       uint64             `json:"revision"`
	Waits          map[string]Wait    `json:"waits,omitempty"`
	Decisions      []GoalDecision     `json:"decisions,omitempty"`
	Task           Task               `json:"task"`
	State          string             `json:"state"`
	Run            *Run               `json:"run,omitempty"`
	Active         bool               `json:"active"`
	TurnStatus     string             `json:"turn_status,omitempty"`
	Receipt        *Receipt           `json:"receipt,omitempty"`
	Verdict        string             `json:"verdict,omitempty"`
	Applied        bool               `json:"applied"`
	Stages         []Stage            `json:"stages"`
}

// AcceptedCompletion retains Core's prepared assessment until Git application.
type AcceptedCompletion struct {
	Head       string            `json:"head"`
	Assessment result.Completion `json:"assessment"`
}

// Event has one payload selected by Kind. Events are admitted by Service only;
// clients and workers never append arbitrary events or acceptance judgments.
type Event struct {
	Occurrence     *canon.RecordOccurrence   `json:"occurrence,omitempty"`
	DoneRequest    *result.CompletionRequest `json:"done_request,omitempty"`
	DoneAcceptance *AcceptedCompletion       `json:"done_acceptance,omitempty"`
	Completion     *result.Completion        `json:"completion,omitempty"`
	Head           string                    `json:"head,omitempty"`
	FileSnapshot   *FileSnapshot             `json:"file_snapshot,omitempty"`
	FileEvidence   *FileEvidence             `json:"file_evidence,omitempty"`
	FileCopy       *FileCopy                 `json:"file_copy,omitempty"`
	Materials      []Material                `json:"materials,omitempty"`
	Wait           *Wait                     `json:"wait,omitempty"`
	Decision       *GoalDecision             `json:"decision,omitempty"`
	Key            string                    `json:"key"`
	TaskID         string                    `json:"task_id"`
	Kind           string                    `json:"kind"`
	Task           *Task                     `json:"task,omitempty"`
	Run            *Run                      `json:"run,omitempty"`
	Receipt        *Receipt                  `json:"receipt,omitempty"`
	RunID          string                    `json:"run_id,omitempty"`
	Value          string                    `json:"value,omitempty"`
	ReceiptID      string                    `json:"receipt_id,omitempty"`
	Evidence       string                    `json:"evidence,omitempty"`
}
