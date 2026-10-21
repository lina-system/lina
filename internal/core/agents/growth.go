// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrGrowthUndone = errors.New("growth_step_was_undone")

// GrowthCandidate is the complete next growth snapshot, never a set of persona
// layers. The consuming Core flow supplies the agent and person, not the worker.
type GrowthCandidate struct {
	ID       string `json:"id"`
	Expected int64  `json:"expected"`
	Snapshot string `json:"snapshot"`
	Notice   string `json:"notice"`
}

type GrowthUndo struct {
	Revision int64 `json:"revision"`
}

// GrowthNotice is durable alongside the revision. Consumers identify a notice
// by (agent, revision), present its one line, and dispatch Undo in one action.
type GrowthNotice struct {
	Revision int64      `json:"revision"`
	Notice   string     `json:"notice"`
	Undo     GrowthUndo `json:"undo"`
}

func growthHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func growthLine(text string) bool {
	return present(text) && utf8.ValidString(text) && !strings.ContainsFunc(text, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	})
}

func growthSuppressed(ctx context.Context, tx *canon.Tx, id, candidate, hash string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_growth_steps
 WHERE agent_id=? AND undone_revision IS NOT NULL AND (candidate_id=? OR content_hash=?)`, id, candidate, hash).Scan(&n)
	return n != 0, err
}

func (s *Store) revisionTx(ctx context.Context, tx *canon.Tx, id string, rev int64) (Preparation, error) {
	p := Preparation{AgentID: id, Revision: rev}
	err := found(tx.QueryRowContext(ctx, "SELECT commit_id FROM agent_revisions WHERE agent_id=? AND revision=?", id, rev).Scan(&p.Commit))
	if err == nil {
		p.Layers, err = s.load(id, p.Commit)
	}
	return p, err
}

func (s *Store) AdoptGrowth(ctx context.Context, id, person string, c GrowthCandidate) (GrowthNotice, error) {
	var out GrowthNotice
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		out, err = s.AdoptGrowthTx(ctx, tx, id, person, c)
		return err
	})
	if err != nil {
		return GrowthNotice{}, err
	}
	return out, nil
}

// AdoptGrowthTx participates in the Moirai receipt/watermark transaction. Saved
// Git objects are immutable; only commit publishes the revision and its notice.
// Callers must not expose the result until their enclosing transaction commits.
func (s *Store) AdoptGrowthTx(ctx context.Context, tx *canon.Tx, id, person string, c GrowthCandidate) (GrowthNotice, error) {
	if tx == nil || !present(person) || !growthLine(c.ID) || c.Expected <= 0 || !utf8.ValidString(c.Snapshot) || !growthLine(c.Notice) {
		return GrowthNotice{}, ErrInvalid
	}
	a, err := get(ctx, tx, id, false)
	if err != nil {
		return GrowthNotice{}, err
	}
	hash := growthHash(c.Snapshot)
	if suppressed, err := growthSuppressed(ctx, tx, id, c.ID, hash); err != nil {
		return GrowthNotice{}, err
	} else if suppressed {
		return GrowthNotice{}, ErrGrowthUndone
	}
	var rev, previous int64
	var savedHash, notice string
	err = tx.QueryRowContext(ctx, `SELECT revision,previous_revision,content_hash,notice FROM agent_growth_steps
 WHERE agent_id=? AND candidate_id=?`, id, c.ID).Scan(&rev, &previous, &savedHash, &notice)
	if err == nil {
		if previous != c.Expected || savedHash != hash || notice != c.Notice {
			return GrowthNotice{}, ErrConflict
		}
		return GrowthNotice{rev, notice, GrowthUndo{rev}}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return GrowthNotice{}, err
	}
	if a.Revision != c.Expected {
		return GrowthNotice{}, ErrConflict
	}
	old, err := s.revisionTx(ctx, tx, id, a.Revision)
	if err != nil {
		return GrowthNotice{}, err
	}
	if old.Layers.GrowthSnapshot == c.Snapshot {
		return GrowthNotice{}, ErrConflict
	}
	layers := old.Layers
	layers.GrowthSnapshot = c.Snapshot
	commit, err := s.save(ctx, id, old.Commit, layers, person)
	if err != nil {
		return GrowthNotice{}, err
	}
	rev = a.Revision + 1
	if err = publish(ctx, tx, id, rev, commit, person); err != nil {
		return GrowthNotice{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_growth_steps
 (agent_id,candidate_id,revision,previous_revision,content_hash,notice) VALUES(?,?,?,?,?,?)`, id, c.ID, rev, a.Revision, hash, c.Notice)
	if err != nil {
		return GrowthNotice{}, err
	}
	return GrowthNotice{rev, c.Notice, GrowthUndo{rev}}, nil
}

// UndoGrowth restores only the previous growth text. Unrelated persona edits
// survive; a later growth change must be undone first. Replays are idempotent.
func (s *Store) UndoGrowth(ctx context.Context, id, person string, u GrowthUndo) (Agent, error) {
	if !present(person) || u.Revision <= 0 {
		return Agent{}, ErrInvalid
	}
	var out Agent
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		a, err := get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		var previous int64
		var undone sql.NullInt64
		var hash string
		err = found(tx.QueryRowContext(ctx, `SELECT previous_revision,content_hash,undone_revision FROM agent_growth_steps
 WHERE agent_id=? AND revision=?`, id, u.Revision).Scan(&previous, &hash, &undone))
		if err != nil {
			return err
		}
		if undone.Valid {
			a.Revision = undone.Int64
			out = a
			return nil
		}
		var later int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_growth_steps WHERE agent_id=? AND revision>? AND undone_revision IS NULL", id, u.Revision).Scan(&later); err != nil {
			return err
		}
		if later != 0 {
			return ErrConflict
		}
		current, err := s.revisionTx(ctx, tx, id, a.Revision)
		if err != nil {
			return err
		}
		if growthHash(current.Layers.GrowthSnapshot) != hash {
			return ErrConflict
		}
		before, err := s.revisionTx(ctx, tx, id, previous)
		if err != nil {
			return err
		}
		if suppressed, err := growthSuppressed(ctx, tx, id, "", growthHash(before.Layers.GrowthSnapshot)); err != nil {
			return err
		} else if suppressed {
			return ErrGrowthUndone
		}
		layers := current.Layers
		layers.GrowthSnapshot = before.Layers.GrowthSnapshot
		commit, err := s.save(ctx, id, current.Commit, layers, person)
		if err != nil {
			return err
		}
		a.Revision++
		if err = publish(ctx, tx, id, a.Revision, commit, person); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE agent_growth_steps SET undone_revision=? WHERE agent_id=? AND revision=?", a.Revision, id, u.Revision); err != nil {
			return err
		}
		out = a
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	return out, nil
}

// GrowthNotices reads committed, non-undone steps, including after restart.
func (s *Store) GrowthNotices(ctx context.Context, id string) ([]GrowthNotice, error) {
	out := []GrowthNotice{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT revision,notice FROM agent_growth_steps WHERE agent_id=? AND undone_revision IS NULL ORDER BY revision", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n GrowthNotice
			if err := rows.Scan(&n.Revision, &n.Notice); err != nil {
				return err
			}
			n.Undo.Revision = n.Revision
			out = append(out, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
