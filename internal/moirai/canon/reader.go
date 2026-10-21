// SPDX-License-Identifier: Apache-2.0

// Package canon exposes only job snapshots from an existing read-only database.
// It cannot import Core's writer and never executes a migration or opens secrets.
package canon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

type Reader struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Reader, error) {
	if path == "" {
		return nil, jobapi.ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro"}
	db, err := driver.Open(uri.String(), func(c *sqlite3.Conn) error { return c.Exec("PRAGMA query_only=ON") })
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Reader{db}, nil
}
func (r *Reader) Close() error { return r.db.Close() }

// Ready reads epoch, policy, watermark and next input in one SQLite snapshot.
// It returns at most one item per job; callers never infer progress from memory.
func (r *Reader) Ready(ctx context.Context, now time.Time) ([]jobapi.Work, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var epochText string
	if err = tx.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&epochText); err != nil {
		return nil, err
	}
	epoch, err := strconv.ParseUint(epochText, 10, 64)
	if err != nil || epoch == 0 {
		return nil, jobapi.ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT record FROM moirai_jobs ORDER BY id")
	if err != nil {
		return nil, err
	}
	var jobs []jobapi.Job
	for rows.Next() {
		var data []byte
		var j jobapi.Job
		if err = rows.Scan(&data); err == nil {
			err = json.Unmarshal(data, &j)
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		if j.Enabled && !now.Before(j.NextRun) {
			jobs = append(jobs, j)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	var work []jobapi.Work
	for _, j := range jobs {
		var task jobapi.Task
		task.Sequence = j.Watermark + 1
		var input []byte
		err = tx.QueryRowContext(ctx, "SELECT input FROM moirai_tasks WHERE job=? AND sequence=?", j.ID, task.Sequence).Scan(&input)
		task.Input = input
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		work = append(work, jobapi.Work{Epoch: epoch, Job: j, Task: task})
	}
	return work, tx.Commit()
}
