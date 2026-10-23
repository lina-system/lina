// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
)

// Citation contains identity and offsets only. Revision strings also support
// the opaque Git revisions of file-backed records; numeric revisions use decimal.
type Citation struct {
	Ordinal                              int
	TargetKind, TargetID, TargetRevision string
	SpanStart, SpanEnd                   sql.NullInt64
	Precision, Role                      string
}

func citationRows(ctx context.Context, tx *Tx, query string, args ...any) ([]Citation, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Citation
	for rows.Next() {
		var c Citation
		if err := rows.Scan(&c.Ordinal, &c.TargetKind, &c.TargetID, &c.TargetRevision, &c.SpanStart, &c.SpanEnd, &c.Precision, &c.Role); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceCitations records only additions and removals, inside the old writer's
// transaction. Unchanged pointers retain their original added revision.
func ReplaceCitations(ctx context.Context, tx *Tx, kind, id, revision string, desired []Citation) error {
	old, err := citationRows(ctx, tx, `SELECT ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role FROM cite WHERE view_kind=? AND view_id=? AND dropped_revision IS NULL`, kind, id)
	if err != nil {
		return err
	}
	remaining := make(map[Citation]bool, len(desired))
	for _, c := range desired {
		remaining[c] = true
	}
	for _, c := range old {
		if remaining[c] {
			delete(remaining, c)
			continue
		}
		// No writer may change an already-published revision in place.
		var added string
		if err := tx.QueryRowContext(ctx, "SELECT added_revision FROM cite WHERE view_kind=? AND view_id=? AND ordinal=? AND dropped_revision IS NULL", kind, id, c.Ordinal).Scan(&added); err != nil {
			return err
		}
		if added == revision {
			return errors.New("citation_revision_conflict")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE cite SET dropped_revision=? WHERE view_kind=? AND view_id=? AND ordinal=? AND dropped_revision IS NULL", revision, kind, id, c.Ordinal); err != nil {
			return err
		}
	}
	for _, c := range desired {
		if !remaining[c] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, kind, id, revision, c.Ordinal, c.TargetKind, c.TargetID, c.TargetRevision, c.SpanStart, c.SpanEnd, c.Precision, c.Role); err != nil {
			return err
		}
		delete(remaining, c)
	}
	return nil
}

// WriteEvidenceCitations uses the same normalization as the migration. The raw
// array is read for identities and span lengths; none of its text is stored.
func WriteEvidenceCitations(ctx context.Context, tx *Tx, kind, id string, revision int64, evidence []byte) error {
	refs, err := citationRows(ctx, tx, `SELECT p.ordinal,`+evidenceCitationColumns+` FROM (SELECT key AS ordinal,value AS evidence FROM json_each(?) WHERE type='object') p`, string(evidence))
	if err != nil {
		return err
	}
	return ReplaceCitations(ctx, tx, kind, id, strconv.FormatInt(revision, 10), refs)
}

// WriteSummaryCitations runs after every changed child has been rebuilt, so a
// parent's provenance names the child revision it actually consumed.
func WriteSummaryCitations(ctx context.Context, tx *Tx, id string, revision int64) error {
	refs, err := citationRows(ctx, tx, `SELECT ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role FROM (`+summaryCitationSelect+`) WHERE view_id=? ORDER BY ordinal`, id)
	if err != nil {
		return err
	}
	return ReplaceCitations(ctx, tx, "summary", id, strconv.FormatInt(revision, 10), refs)
}

func BriefCitationID(person, agent string) string {
	raw, _ := json.Marshal([]string{person, agent})
	return string(raw)
}
