// SPDX-License-Identifier: Apache-2.0
package recall

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

const BatchSize = 128

var ErrBytesUnavailable = errors.New("recall_bytes_unavailable")

type Canon interface {
	Read(context.Context, func(*canon.Tx) error) error
	Write(context.Context, func(*canon.Tx) error) error
}

// ReadEvent resolves derived event text, including imported material bodies.
// It is called only after the read transaction has closed.
type ReadEvent func(context.Context, string) (string, error)
type Store struct {
	db        Canon
	readEvent ReadEvent
	after     int64
}

func New(db Canon, readEvent ReadEvent) *Store { return &Store{db: db, readEvent: readEvent} }

type pending struct {
	sequence       int64
	kind, id, text string
	external       bool
	spans          []secrets.Span
}

// DrainBatch reads at most 128 durable candidates. A failed material remains
// queued, but does not prevent other rows in this batch from being published.
// The generation check fences concurrent revisions, removals and drains.
func (s *Store) DrainBatch(ctx context.Context) (int, error) {
	var batch []pending
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT sequence,kind,source_id FROM recall_pending WHERE sequence>? ORDER BY sequence LIMIT ?", s.after, BatchSize)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p pending
			if err = rows.Scan(&p.sequence, &p.kind, &p.id); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, p)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for i := range batch {
			p := &batch[i]
			src, err := readSource(ctx, tx, p.kind, p.id)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			p.text, p.external = src.body.String, !src.body.Valid
			if p.kind != "event" {
				continue
			}
			rows, err := tx.QueryContext(ctx, "SELECT start,end,sensitivity FROM ev_span WHERE event_id=? AND sensitivity='secret' ORDER BY start,end", p.id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var span secrets.Span
				if err = rows.Scan(&span.Start, &span.End, &span.Sensitivity); err != nil {
					rows.Close()
					return err
				}
				p.spans = append(p.spans, span)
			}
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		s.after = 0
		return 0, nil
	}
	s.after = batch[len(batch)-1].sequence
	completed := 0
	var failures error
	for _, p := range batch {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		text := p.text
		if p.external {
			err = ErrBytesUnavailable
			if s.readEvent != nil {
				text, err = s.readEvent(ctx, p.id)
			}
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
		} else {
			masked, e := secrets.Mask(text, p.spans...)
			if e != nil {
				failures = errors.Join(failures, e)
				continue
			}
			text = masked.DerivedText()
		}
		err = s.db.Write(ctx, func(tx *canon.Tx) error {
			var sequence int64
			e := tx.QueryRowContext(ctx, "SELECT sequence FROM recall_pending WHERE kind=? AND source_id=?", p.kind, p.id).Scan(&sequence)
			if errors.Is(e, sql.ErrNoRows) {
				return nil
			}
			if e != nil {
				return e
			}
			if sequence != p.sequence {
				return nil
			}
			if e = Index(ctx, tx, p.kind, p.id, text); e == nil {
				completed++
			}
			return e
		})
		if err != nil {
			return completed, errors.Join(failures, err)
		}
	}
	return completed, failures
}

// Run is owned and joined by Core, outside every request path. Idle/error
// retries wait one second; successful full batches yield for 10 milliseconds.
func (s *Store) Run(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := s.DrainBatch(ctx)
		delay := time.Second
		if err == nil && n == BatchSize {
			delay = 10 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
