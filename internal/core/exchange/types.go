// SPDX-License-Identifier: Apache-2.0

// Package exchange owns the durable mailbox and agent-exchange admission
// to the conversation ledger. It has no person-conversation admission or personal-knowledge port.
package exchange

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

var (
	ErrInvalid  = errors.New("invalid_exchange")
	ErrClosed   = errors.New("exchange_closed")
	ErrConflict = errors.New("exchange_delivery_conflict")
	ErrCheck    = errors.New("exchange_check_failed")
	ErrLimit    = errors.New("exchange_round_limit")
	ErrNotOwner = errors.New("exchange_decision_not_owned_by_person")
)

const RoundLimit = 4

type State string

const (
	Queued    State = "queued"
	Delivered State = "delivered"
	Read      State = "read"
	Answered  State = "answered"
)

type Outcome string

const (
	Pending      Outcome = "pending"
	Acknowledged Outcome = "acknowledged"
	Checked      Outcome = "checked"
	Expired      Outcome = "expired"
	Limited      Outcome = "round_limit"
)

type OwnerKind string

const (
	AgentOwner  OwnerKind = "agent"
	PersonOwner OwnerKind = "person"
)

type Owner struct {
	Kind OwnerKind
	ID   string
}

// ResolveOwner is supplied by Core's authority boundary, never from model output
// or hand-off content. Resolve through the supplied transaction so ownership
// and the waiting flag share one snapshot. Never start a nested canon write.
// An absent resolver cannot set a waiting flag.
type DecisionRef struct {
	ExchangeID, DeliveryID string
	Decision               Decision
}

type ResolveOwner func(context.Context, *canon.Tx, DecisionRef) (Owner, error)
type Check func(context.Context, string, string, Answer) (bool, error)

type Material struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}
type Decision struct {
	Question       string `json:"question"`
	Recommendation string `json:"recommendation"`
}
type HandOff struct {
	Subject    string     `json:"subject"`
	Wanted     string     `json:"wanted"`
	OutOfScope string     `json:"out_of_scope"`
	Done       string     `json:"done"`
	Check      string     `json:"check"`
	Deadline   time.Time  `json:"deadline"`
	Materials  []Material `json:"materials,omitempty"`
	Decision   *Decision  `json:"decision,omitempty"`
}
type Request struct {
	DeliveryID, From, To string
	HandOff              HandOff
	Origin               *Origin
}
type Receipt struct {
	ID, DeliveryID string
	Inserted       bool
}
type Evidence struct {
	EventID  string    `json:"event_id,omitempty"`
	Material Material  `json:"material,omitempty"`
	At       time.Time `json:"at"`
}
type Answer struct {
	Text     string     `json:"text"`
	Evidence []Evidence `json:"evidence"`
}
type Record struct {
	ID, DeliveryID, From, To string
	HandOff                  HandOff
	Round                    int
	State                    State
	Outcome                  Outcome
	NeedsPerson              bool
	Updated                  time.Time
}
type Event struct {
	ID                      int64
	DeliveryID, Kind, Agent string
	At                      time.Time
	Message                 guard.Message
}
type Line struct {
	ID, From, To, Subject string
	State                 State
	Outcome               Outcome
	NeedsPerson           bool
	At                    time.Time
}
type Waiting struct{ ExchangeID, PersonID, Question string }
type Canon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}
type Store struct {
	db        Canon
	materials ledger.Materials
}

func New(db Canon) *Store { return NewWithMaterials(db, nil) }

// NewWithMaterials supports ledger bodies above the inline threshold.
func NewWithMaterials(db Canon, materials ledger.Materials) *Store {
	return &Store{db: db, materials: materials}
}

func identifier(s string) bool {
	if len(s) == 0 || len(s) > 256 || !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return false
	}
	// Opaque identifiers must retain their exact meaning. Refuse secret-shaped
	// values instead of masking them into a different reference or check name.
	masked, err := secrets.Mask(s)
	return err == nil && masked.Text() == s
}
func present(s string) bool          { return strings.TrimSpace(s) != "" }
func line(s string) bool             { return present(s) && strings.IndexFunc(s, unicode.IsControl) < 0 }
func timestamp(t time.Time) bool     { _, err := t.MarshalJSON(); return !t.IsZero() && err == nil }
func clean(s string) (string, error) { m, err := secrets.Mask(s); return m.Text(), err }
func normalize(r Request, at time.Time) (Request, error) {
	if !identifier(r.DeliveryID) || !identifier(r.From) || !identifier(r.To) || r.From == r.To || !timestamp(at) || !timestamp(r.HandOff.Deadline) || !r.HandOff.Deadline.After(at) {
		return Request{}, ErrInvalid
	}
	h := r.HandOff
	if !line(h.Subject) || !present(h.Wanted) || !present(h.OutOfScope) || !present(h.Done) || !identifier(h.Check) {
		return Request{}, ErrInvalid
	}
	h.Materials = append([]Material(nil), h.Materials...)
	seen := map[Material]bool{}
	for _, m := range h.Materials {
		if !identifier(m.ID) || !identifier(m.Revision) || seen[m] {
			return Request{}, ErrInvalid
		}
		seen[m] = true
	}
	for _, p := range []*string{&h.Subject, &h.Wanted, &h.OutOfScope, &h.Done} {
		var err error
		*p, err = clean(*p)
		if err != nil {
			return Request{}, ErrInvalid
		}
	}
	if h.Decision != nil {
		d := *h.Decision
		if !line(d.Question) || !line(d.Recommendation) {
			return Request{}, ErrInvalid
		}
		var err error
		d.Question, err = clean(d.Question)
		if err != nil {
			return Request{}, ErrInvalid
		}
		d.Recommendation, err = clean(d.Recommendation)
		if err != nil {
			return Request{}, ErrInvalid
		}
		h.Decision = &d
	}
	r.HandOff = h
	return r, nil
}
func normalizeAnswer(a Answer) (Answer, error) {
	if !present(a.Text) || len(a.Evidence) == 0 {
		return Answer{}, ErrInvalid
	}
	var err error
	a.Text, err = clean(a.Text)
	if err != nil {
		return Answer{}, ErrInvalid
	}
	a.Evidence = append([]Evidence(nil), a.Evidence...)
	for _, e := range a.Evidence {
		if !timestamp(e.At) || e.EventID != "" && !identifier(e.EventID) || e.EventID == "" && e.Material.ID == "" {
			return Answer{}, ErrInvalid
		}
		if e.Material != (Material{}) && (!identifier(e.Material.ID) || !identifier(e.Material.Revision)) {
			return Answer{}, ErrInvalid
		}
	}
	return a, nil
}
