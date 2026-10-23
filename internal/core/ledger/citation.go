// SPDX-License-Identifier: Apache-2.0
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Citation reads a quote only after binding its source to the reader. The
// second check closes removal during material I/O. No saved quote is a fallback.
func (s *Store) Citation(ctx context.Context, person, agent string, ref canon.Citation) (canon.CitationValue, error) {
	check := func() (canon.CitationValue, error) {
		value := canon.CitationValue{}
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			var state string
			tomb := canon.CitationTombstone{}
			err := tx.QueryRowContext(ctx, `SELECT state,person,agent,at FROM retract_target WHERE kind='event' AND id=?`, ref.TargetID).Scan(&state, &tomb.Person, &tomb.Agent, &tomb.At)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && state != "active" {
				if tomb.Person != person || tomb.Agent != agent {
					value.Status = "restricted"
					return nil
				}
				if state == "erased" {
					value.Status = "tombstone"
					value.Tombstone = &tomb
				} else {
					value.Status = "retracted"
				}
				return nil
			}
			var owner string
			var who sql.NullString
			err = tx.QueryRowContext(ctx, `SELECT agent_id,person_id FROM conv_event WHERE id=?`, ref.TargetID).Scan(&owner, &who)
			if errors.Is(err, sql.ErrNoRows) {
				value.Status = "bytes_unavailable"
				return nil
			}
			if err != nil {
				return err
			}
			if owner != agent || who.Valid && who.String != person {
				value.Status = "restricted"
				return nil
			}
			var visible int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM retract_visible_event WHERE id=?`, ref.TargetID).Scan(&visible); err != nil {
				return err
			}
			if visible == 0 {
				value.Status = "retracted"
			}
			return nil
		})
		return value, err
	}
	if ref.TargetKind != "event" || person == "" || agent == "" {
		return canon.CitationValue{Status: "restricted"}, nil
	}
	value, err := check()
	if err != nil || value.Status != "" {
		return value, err
	}
	event, err := s.Get(ctx, ref.TargetID)
	if ctx.Err() != nil {
		return canon.CitationValue{}, ctx.Err()
	}
	if errors.Is(err, ErrBytesUnavailable) || errors.Is(err, sql.ErrNoRows) {
		return canon.CitationValue{Status: "bytes_unavailable"}, nil
	}
	if err != nil {
		return canon.CitationValue{}, err
	}
	value, err = check()
	if err != nil || value.Status != "" {
		return value, err
	}
	text := event.Body.DerivedText()
	if ref.SpanStart.Valid != ref.SpanEnd.Valid {
		return canon.CitationValue{Status: "bytes_unavailable"}, nil
	}
	if ref.SpanStart.Valid {
		start, end := ref.SpanStart.Int64, ref.SpanEnd.Int64
		if start < 0 || end < start || end > int64(len(text)) || !utf8.ValidString(text[:start]) || !utf8.ValidString(text[start:end]) {
			return canon.CitationValue{Status: "bytes_unavailable"}, nil
		}
		text = text[start:end]
	}
	return canon.CitationValue{Quote: text}, nil
}
