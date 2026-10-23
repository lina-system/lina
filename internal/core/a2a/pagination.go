// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Page bounds row decoding even when a caller has a long retained task history.
// The count and page use the same authorized view in one read transaction.
func (s *Store) Page(ctx context.Context, p Publication, b Binding, pr Principal, q ListRequest, cur cursor, size int) ([]Record, int, error) {
	fingerprints := []string{}
	for _, id := range b.Definitions {
		d, err := p.Definition(b, id)
		if err == nil {
			fingerprints = append(fingerprints, Fingerprint(p, b, d))
		}
	}
	if len(fingerprints) == 0 {
		return nil, 0, nil
	}
	where := "publication=? AND caller=? AND json_extract(record,'$.principal.Policy')=? AND json_extract(record,'$.authority') IN (" + strings.TrimRight(strings.Repeat("?,", len(fingerprints)), ",") + ")"
	args := []any{p.ID, pr.Key(), pr.Policy}
	for _, fingerprint := range fingerprints {
		args = append(args, fingerprint)
	}
	if q.ContextID != "" {
		where += " AND json_extract(record,'$.task.contextId')=?"
		args = append(args, q.ContextID)
	}
	if q.Status != "" {
		where += " AND json_extract(record,'$.task.status.state')=?"
		args = append(args, q.Status)
	}
	if q.StatusTimestampAfter != "" {
		after, err := time.Parse(time.RFC3339Nano, q.StatusTimestampAfter)
		if err != nil {
			return nil, 0, ErrInvalid
		}
		where += " AND updated>=?"
		args = append(args, after.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	}
	records := []Record{}
	total := 0
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM a2a_tasks WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		pageWhere := where
		pageArgs := append([]any{}, args...)
		if cur.ID != "" {
			pageWhere += " AND (updated<? OR (updated=? AND id<?))"
			pageArgs = append(pageArgs, cur.Updated, cur.Updated, cur.ID)
		}
		pageArgs = append(pageArgs, size+1)
		rows, err := tx.QueryContext(ctx, "SELECT record FROM a2a_tasks WHERE "+pageWhere+" ORDER BY updated DESC,id DESC LIMIT ?", pageArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var r Record
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &r); err != nil {
				return err
			}
			records = append(records, r)
		}
		return rows.Err()
	})
	return records, total, err
}
