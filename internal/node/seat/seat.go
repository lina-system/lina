// SPDX-License-Identifier: Apache-2.0

// Package seat fences GUI work with an explicit, exclusive handover. The Node
// host supplies authenticated consent, executor observations and a durable
// receipt journal; this package neither issues grants nor observes the OS.
package seat

import (
	"context"
	"crypto/rand"
	"errors"
	"maps"
	"slices"
	"sync"

	"github.com/thisisjun786/lina/kit/grant"
)

type Kind string

const (
	HumanInput Kind = "human_input"
	Locked     Kind = "locked"
	Unlocked   Kind = "unlocked"
	Resumed    Kind = "resumed"
	Expired    Kind = "expired"
	Revoked    Kind = "revoked"
	EventsLost Kind = "events_lost"
)

var (
	ErrConsent = errors.New("seat: explicit handover required")
	ErrBusy    = errors.New("seat: session held by another lease")
	ErrLocked  = errors.New("seat: session locked")
	ErrEnded   = errors.New("seat: grant already consumed or lease ended")
	ErrJournal = errors.New("seat: receipt journal unavailable")
	ErrEvents  = errors.New("seat: OS event monitoring unavailable")
	ErrEvent   = errors.New("seat: invalid OS event")
)

// Consent comes only from the authenticated person's handover, never from model
// arguments. A fresh grant needs fresh consent, even for the same task/session.
type Consent struct {
	GrantID, Person, TaskID, Session string
	Revision                         uint64
}

// Event has no keys, pointer coordinates, clipboard data or screen contents.
// Resumed and EventsLost apply to every session on the Node.
type Event struct {
	Kind    Kind
	Session string
}
type Receipt struct {
	ID, GrantID, MainID, NodeID, NodeBootID, TaskID, AgentID, Person, Session string
	Epoch                                                                     uint64
	Reason                                                                    Kind
}

// Journal must acknowledge only a durable append. A failed append fences future
// acquisitions; cancellation never waits for persistence. Journal methods must
// not call back into the Manager. Persistence/reconciliation belong to the host.
type Journal interface{ Append(Receipt) error }

type Manager struct {
	revision       uint64
	resumeRevision uint64
	revisions      map[string]uint64
	mu             sync.Mutex
	journalMu      sync.Mutex
	pending        map[string]int
	journal        Journal
	live           map[string]*Lease
	consumed       map[string]bool
	locked         map[string]bool
	failure        error
}

func New(journal Journal) *Manager {
	if journal == nil {
		panic("seat: nil journal")
	}
	return &Manager{pending: make(map[string]int), revision: 1, resumeRevision: 1, revisions: make(map[string]uint64), journal: journal, live: make(map[string]*Lease), consumed: make(map[string]bool), locked: make(map[string]bool)}
}

// Revision is captured when the person explicitly hands over the session.
// Human takeover fences even consent that has not acquired a lease yet.
func (m *Manager) Revision(session string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionRevision(session)
}
func (m *Manager) sessionRevision(session string) uint64 {
	if revision := m.revisions[session]; revision != 0 {
		return revision
	}
	return m.resumeRevision
}
func (m *Manager) advance(session string) {
	m.revision++
	if m.revision == 0 {
		m.failure = errors.Join(m.failure, ErrEvents)
		return
	}
	if session == "" {
		m.resumeRevision = m.revision
		clear(m.revisions)
	} else {
		m.revisions[session] = m.revision
	}
}

type Lease struct {
	manager *Manager
	grant   grant.Grant
	ctx     context.Context
	cancel  context.CancelFunc
	ended   bool
}

func (l *Lease) Context() context.Context { return l.ctx }

// Acquire never queues GUI effects. ErrBusy leaves the requesting task waiting;
// it may retry with its still-unused consent, or request another session.
// Environment is an executor observation, not request-supplied authority.
func (m *Manager) Acquire(g grant.Grant, r grant.Request, e grant.Environment, c Consent) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure != nil {
		return nil, m.failure
	}
	if err := grant.Check(g, r, e); err != nil {
		return nil, err
	}
	if m.consumed[r.GrantID] {
		return nil, ErrEnded
	}
	if r.NodeID == "" || r.NodeBootID == "" || r.GUISession == "" || c.GrantID != r.GrantID || c.Person != r.Person || c.TaskID != r.TaskID || c.Session != r.GUISession || c.Revision != m.sessionRevision(r.GUISession) {
		return nil, ErrConsent
	}
	if m.locked[r.GUISession] {
		return nil, ErrLocked
	}
	if m.pending[r.GUISession] > 0 {
		return nil, ErrBusy
	}
	if old := m.live[r.GUISession]; old != nil {
		if e.Now.Before(old.grant.Expires) {
			return nil, ErrBusy
		}
		if _, err := m.end([]*Lease{old}, Expired); err != nil {
			return nil, err
		}
	}
	// Prevent mutation of the caller's grant from widening a live lease.
	g.Scope = slices.Clone(g.Scope)
	g.Revisions = maps.Clone(g.Revisions)
	ctx, cancel := context.WithCancel(context.Background())
	l := &Lease{manager: m, grant: g, ctx: ctx, cancel: cancel}
	m.live[r.GUISession] = l
	m.consumed[r.GrantID] = true
	return l, nil
}

// Check belongs immediately before each GUI effect, and the executor must also
// honor Context cancellation for work in progress. It is not a GUI executor and
// does not claim OS-level exclusion between this check and an input syscall.
func (l *Lease) Check(r grant.Request, e grant.Environment) error {
	m := l.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.ended {
		return ErrEnded
	}
	err := grant.Check(l.grant, r, e)
	if errors.Is(err, grant.ErrExpired) {
		_, journalErr := m.end([]*Lease{l}, Expired)
		return errors.Join(err, journalErr)
	}
	return err
}

// Handle must be called by a trusted OS event adapter. Unlock only makes fresh
// handovers possible; it never restores a consumed grant or a cancelled context.
func (m *Manager) Handle(e Event) ([]Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch e.Kind {
	case HumanInput, Locked, Unlocked:
		if e.Session == "" {
			return nil, ErrEvent
		}
		if e.Kind == Unlocked {
			delete(m.locked, e.Session)
			return nil, nil
		}
		m.advance(e.Session)
		if e.Kind == Locked {
			m.locked[e.Session] = true
		}
		if l := m.live[e.Session]; l != nil {
			return m.end([]*Lease{l}, e.Kind)
		}
		return nil, nil
	case Resumed, EventsLost:
		if e.Session != "" {
			return nil, ErrEvent
		}
		m.advance("")
		if e.Kind == EventsLost {
			m.failure = errors.Join(m.failure, ErrEvents)
		}
		sessions := slices.Sorted(maps.Keys(m.live))
		leases := make([]*Lease, 0, len(sessions))
		for _, session := range sessions {
			leases = append(leases, m.live[session])
		}
		return m.end(leases, e.Kind)
	case Expired, Revoked:
		// These reasons are emitted by grant checks and explicit revocation only.
		return nil, ErrEvent
	default:
		return nil, ErrEvent
	}
}

func (m *Manager) Revoke(grantID string) ([]Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range m.live {
		if l.grant.Identity.GrantID == grantID {
			return m.end([]*Lease{l}, Revoked)
		}
	}
	return nil, nil
}

// Called with mu held. Cancel ALL affected work before the first disk operation,
// including when an earlier append fails. Returned receipts are durable only.
func (m *Manager) end(leases []*Lease, reason Kind) ([]Receipt, error) {
	for _, l := range leases {
		l.ended = true
		l.cancel()
		delete(m.live, l.grant.Identity.GUISession)
		m.pending[l.grant.Identity.GUISession]++
	}
	// Storage must not hold the state mutex: another session can be taken back
	// even while this receipt is blocked. The per-session pending count prevents
	// reacquisition before the end has been persisted.
	m.mu.Unlock()
	m.journalMu.Lock()
	var receipts []Receipt
	var failure error
	for _, l := range leases {
		i := l.grant.Identity
		r := Receipt{ID: rand.Text(), GrantID: i.GrantID, MainID: i.MainID, NodeID: i.NodeID, NodeBootID: i.NodeBootID, Epoch: i.Epoch, TaskID: i.TaskID, AgentID: i.Agent, Person: i.Person, Session: i.GUISession, Reason: reason}
		if err := m.journal.Append(r); err != nil {
			failure = errors.Join(failure, err)
		} else {
			receipts = append(receipts, r)
		}
	}
	m.journalMu.Unlock()
	m.mu.Lock()
	for _, l := range leases {
		session := l.grant.Identity.GUISession
		m.pending[session]--
		if m.pending[session] == 0 {
			delete(m.pending, session)
		}
	}
	if failure != nil {
		m.failure = errors.Join(m.failure, ErrJournal, failure)
	}
	return receipts, failure
}
