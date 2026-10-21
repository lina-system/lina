// SPDX-License-Identifier: Apache-2.0
package lane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"sort"
	"time"
)

type Notice struct {
	ResultRevision                       int64
	Surface                              ledger.Surface
	ID, Run, Person, Agent, Conversation string
	Outcome, Summary, Result, ResultType string
	Landed                               time.Time
}
type Delivery struct {
	ID, Person, Agent, Conversation string
	At                              time.Time
	Notices                         []Notice
}
type NoticeCanon interface {
	Write(context.Context, func(*canon.Tx) error) error
	Read(context.Context, func(*canon.Tx) error) error
}

// NoticeSchema is installed by the composition root as a numbered migration.
// The outbox is durable before Notice returns. Events exist only at delivery.
const NoticeSchema = `
CREATE TABLE lane_notices(id TEXT PRIMARY KEY, conversation TEXT NOT NULL, record BLOB NOT NULL, delivered INTEGER NOT NULL DEFAULT 0);
CREATE INDEX lane_notices_pending ON lane_notices(conversation,delivered);
CREATE TABLE lane_notice_events(seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, conversation TEXT NOT NULL, record BLOB NOT NULL);
`

// NoticeSink appends the system-originated conversation event in the same
// transaction as delivery. It must not emit to clients before that commit.
// Run summaries are external evidence, never instructions or approvals.
type NoticeSink func(context.Context, *canon.Tx, Delivery) error
type Notices struct {
	db   NoticeCanon
	sink NoticeSink
	// OnDelivered publishes the committed event to subscribers. Configure it
	// before starting lanes; it must return promptly and never call the manager.
	OnDelivered func(Delivery)
}

func NewNotices(db NoticeCanon, sink NoticeSink) *Notices { return &Notices{db: db, sink: sink} }
func NewWithNotices(notices *Notices) *Manager            { m := New(); m.notices = notices; return m }

// Notice persists a landing and wakes its conversation's lane. Idle lanes
// deliver immediately; active lanes hold notices until all queued input ends.
func (m *Manager) Notice(ctx context.Context, n Notice) error {
	if m.notices == nil || !validNotice(n) {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	if err := m.notices.put(ctx, n); err != nil {
		return err
	}
	m.wakeNotices(n.Conversation)
	return nil
}
func validNotice(n Notice) bool {
	if n.Surface.Kind == "" {
		return false
	}
	if n.ID == "" || n.Run == "" || n.Person == "" || n.Agent == "" || n.Conversation == "" || n.Landed.IsZero() {
		return false
	}
	switch n.Outcome {
	case "complete", "partial", "failed", "cancelled", "needs_input":
		return true
	}
	return false
}

// wakeNotices requires m.mu. No separate delivery goroutine can race a turn.
func (m *Manager) wakeNotices(id string) {
	lane := m.lanes[id]
	if lane == nil {
		lane = &conversation{}
		m.lanes[id] = lane
		m.workers.Add(1)
		go m.run(id, lane)
	}
	lane.checkNotices = true
}

// ResumeNotices requeues the durable outbox after startup or a storage repair.
func (m *Manager) ResumeNotices(ctx context.Context) error {
	if m.notices == nil {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	ids, err := m.notices.pending(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		m.wakeNotices(id)
	}
	return nil
}
func (s *Notices) put(ctx context.Context, n Notice) error {
	masked, err := secrets.Mask(n.Summary)
	if err != nil {
		return err
	}
	n.Summary = masked.DerivedText()
	n.Landed = n.Landed.UTC()
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var existing []byte
		err := tx.QueryRowContext(ctx, "SELECT record FROM lane_notices WHERE id=?", n.ID).Scan(&existing)
		if err == nil {
			if !bytes.Equal(existing, data) {
				return ErrInvalid
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO lane_notices(id,conversation,record) VALUES(?,?,?)", n.ID, n.Conversation, data)
		return err
	})
}
func (s *Notices) pending(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT conversation FROM lane_notices WHERE delivered=0 ORDER BY conversation")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}
func pendingNotices(ctx context.Context, tx *canon.Tx, conversation string) ([]Notice, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM lane_notices WHERE conversation=? AND delivered=0", conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notices []Notice
	for rows.Next() {
		var data []byte
		var n Notice
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &n); err != nil {
			return nil, err
		}
		notices = append(notices, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(notices, func(i, j int) bool {
		if notices[i].Landed.Equal(notices[j].Landed) {
			return notices[i].ID < notices[j].ID
		}
		return notices[i].Landed.Before(notices[j].Landed)
	})
	return notices, nil
}
func (s *Notices) deliver(ctx context.Context, conversation string) (bool, error) {
	delivered := false
	var committed Delivery
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		pending, err := pendingNotices(ctx, tx, conversation)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		first := pending[0]
		d := Delivery{ID: first.ID, Person: first.Person, Agent: first.Agent, Conversation: conversation, At: time.Now().UTC()}
		for _, n := range pending {
			// A fixed window prevents an endless sequence of arrivals from absorbing
			// arbitrarily distant landings. Different owners never share an event.
			if n.Landed.After(first.Landed.Add(10 * time.Second)) {
				break
			}
			if n.Person == first.Person && n.Agent == first.Agent {
				d.Notices = append(d.Notices, n)
			}
		}
		if err := appendNoticeEvent(ctx, tx, d); err != nil {
			return err
		}
		if s.sink != nil {
			if err := s.sink(ctx, tx, d); err != nil {
				return err
			}
		}
		data, err := json.Marshal(d)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO lane_notice_events(id,conversation,record) VALUES(?,?,?)", d.ID, conversation, data); err != nil {
			return err
		}
		for _, n := range d.Notices {
			if _, err := tx.ExecContext(ctx, "UPDATE lane_notices SET delivered=1 WHERE id=?", n.ID); err != nil {
				return err
			}
		}
		delivered = true
		committed = d
		return nil
	})
	if delivered && err == nil && s.OnDelivered != nil {
		s.OnDelivered(committed)
	}
	return delivered && err == nil, err
}
func (s *Notices) Delivered(ctx context.Context, conversation string) ([]Delivery, error) {
	var deliveries []Delivery
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM lane_notice_events WHERE conversation=? ORDER BY seq", conversation)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			var d Delivery
			if err := rows.Scan(&data); err != nil {
				return err
			}
			if err := json.Unmarshal(data, &d); err != nil {
				return err
			}
			deliveries = append(deliveries, d)
		}
		return rows.Err()
	})
	return deliveries, err
}

// Reuse the caller's fenced transaction: the ledger never opens another writer.
type noticeTransaction struct{ tx *canon.Tx }

func (t noticeTransaction) Write(_ context.Context, f func(*canon.Tx) error) error { return f(t.tx) }
func (t noticeTransaction) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(t.tx) }
func appendNoticeEvent(ctx context.Context, tx *canon.Tx, d Delivery) error {
	digest := sha256.Sum256([]byte(d.ID))
	eventID := "background-notice-" + hex.EncodeToString(digest[:])
	body, err := json.Marshal(d.Notices)
	if err != nil {
		return err
	}
	_, err = ledger.New(noticeTransaction{tx}, nil).Admit(ctx, guard.EntryBackgroundNotice, ledger.Input{
		ID: eventID, ConversationID: d.Conversation, AgentID: d.Agent, Surface: d.Notices[0].Surface,
		ScaffoldKind: "background_notice", OccurredAt: d.At, RecordedAt: d.At, TimeQuality: "recorded",
		Locator: "background-notice:" + d.ID, Body: string(body),
	})
	return err
}
