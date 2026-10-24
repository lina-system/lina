// SPDX-License-Identifier: Apache-2.0
package recall

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// drainWindows builds at most 120 records of a queued chat. New admission,
// retraction and consent changes replace the queue sequence and fence the read.
func (s *Store) drainWindows(ctx context.Context) (int, error) {
	var sequence int64
	var offset int
	var platform, account, chat, person string
	var members []Hit
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT q.sequence,q.offset,q.platform,q.account,q.chat,s.person FROM messenger_window_pending q JOIN messenger_source s ON s.platform=q.platform AND s.account=q.account WHERE q.sequence>? ORDER BY q.sequence LIMIT 1`, s.windowAfter).Scan(&sequence, &offset, &platform, &account, &chat, &person)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT m.event,cast(m.revision AS TEXT),m.occurred_at,e.conversation_id FROM messenger_record m JOIN messenger_chat c ON c.platform=m.platform AND c.account=m.account AND c.chat=m.chat JOIN retract_visible_event e ON e.id=m.event WHERE m.platform=? AND m.account=? AND m.chat=? AND m.state='live' AND c.embed=1 AND EXISTS(SELECT 1 FROM messenger_embed_source es WHERE es.platform=m.platform AND es.account=m.account AND es.person=?) ORDER BY julianday(m.occurred_at),m.event LIMIT 121 OFFSET ?`, platform, account, chat, person, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h := Hit{Kind: Event}
			var at string
			if err := rows.Scan(&h.ID, &h.Revision, &at, &h.Holder); err != nil {
				return err
			}
			h.OccurredAt, _ = time.Parse(time.RFC3339Nano, at)
			members = append(members, h)
		}
		return rows.Err()
	})
	if errors.Is(err, sql.ErrNoRows) {
		s.windowAfter = 0
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s.windowAfter = sequence
	var groups [][]Hit
	for _, h := range members {
		if len(groups) == 0 || len(groups[len(groups)-1]) == 10 || h.OccurredAt.Sub(groups[len(groups)-1][len(groups[len(groups)-1])-1].OccurredAt) > 5*time.Minute {
			groups = append(groups, []Hit{})
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], h)
	}
	more := len(members) == 121
	if more {
		groups = groups[:len(groups)-1]
	}
	texts := make([]string, len(groups))
	for i, group := range groups {
		parts := []string{}
		for _, h := range group {
			text, err := sourceText(ctx, s.db, s.readEvent, h)
			if err != nil {
				return 0, err
			}
			parts = append(parts, text)
		}
		texts[i] = strings.Join(parts, "\n---\n")
	}
	processed := 0
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		var present int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM messenger_window_pending WHERE sequence=? AND offset=?", sequence, offset).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			return nil
		}
		for i, group := range groups {
			keys := []string{}
			citations := []canon.Citation{}
			for ordinal, h := range group {
				keys = append(keys, h.ID+":"+h.Revision)
				citations = append(citations, canon.Citation{Ordinal: ordinal, TargetKind: "event", TargetID: h.ID, TargetRevision: "0", Precision: "event", Role: "covers"})
			}
			id := "messenger-window:" + textHash(strings.Join(keys, "\n"))
			if _, err := tx.ExecContext(ctx, "INSERT INTO messenger_window(id,platform,account,chat) VALUES(?,?,?,?)", id, platform, account, chat); err != nil {
				return err
			}
			if err := canon.ReplaceCitations(ctx, tx, "summary", id, "1", citations); err != nil {
				return err
			}
			last := group[len(group)-1]
			if _, err := tx.ExecContext(ctx, `INSERT INTO recall_doc(kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay,text_hash) VALUES('summary',?,'1',?,'all',?,?,'','{"kind":"messenger"}',0,?)`, id, person, last.Holder, last.OccurredAt.UTC().Format(time.RFC3339Nano), textHash(texts[i])); err != nil {
				return err
			}
			processed += len(group)
		}
		if more {
			_, err := tx.ExecContext(ctx, "UPDATE messenger_window_pending SET offset=offset+? WHERE sequence=?", processed, sequence)
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM messenger_window_pending WHERE sequence=?", sequence)
		return err
	})
	if err == nil && more {
		s.windowAfter = sequence - 1
	}
	return processed, err
}

func windowText(ctx context.Context, db Canon, read ReadEvent, id string) (string, error) {
	var ids []string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.target_id FROM messenger_window w JOIN cite c ON c.view_kind='summary' AND c.view_id=w.id AND c.dropped_revision IS NULL WHERE w.id=? ORDER BY c.ordinal`, id)
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
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", ErrBytesUnavailable
	}
	parts := []string{}
	for _, id := range ids {
		text, err := sourceText(ctx, db, read, Hit{Kind: Event, ID: id})
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n---\n"), nil
}
