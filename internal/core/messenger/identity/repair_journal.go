// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// repairIntent is the link change journaled with a retraction. Recovery writes
// these rows when the canon transaction rolled back after the removal list
// reached disk. The fields are ids and actions. Answer text and signal values
// stay in canon, not in this append-only list.
type repairIntent struct {
	Version  int             `json:"version"`
	Links    []repairLink    `json:"links,omitempty"`
	Drops    []repairDrop    `json:"drops,omitempty"`
	Requeues []repairRequeue `json:"requeues,omitempty"`
	Question *repairQuestion `json:"question,omitempty"`
}

type repairLink struct {
	ID        string         `json:"id"`
	Platform  string         `json:"platform"`
	Scope     string         `json:"scope"`
	User      string         `json:"user"`
	Record    string         `json:"record"`
	Class     string         `json:"class"`
	Maker     string         `json:"maker"`
	CreatedAt string         `json:"created_at"`
	Revision  int            `json:"revision"`
	State     string         `json:"state"`
	From      string         `json:"valid_from"`
	Through   string         `json:"valid_through"`
	Action    string `json:"action"`
	Actor     string `json:"actor"`
	At        string `json:"at"`
}

type repairDrop struct {
	Link string `json:"link"`
	ID   string `json:"id"`
}

type repairRequeue struct {
	Platform string `json:"platform"`
	Scope    string `json:"scope"`
	User     string `json:"user"`
	From     string `json:"from"`
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

type repairQuestion struct {
	Platform string `json:"platform"`
	Scope    string `json:"scope"`
	User     string `json:"user"`
	State    string `json:"state"`
}

// repairIntent reads the link rows a repair just wrote so the removal list can
// replay them. question records the person's answer when this repair settled one.
func (s *Store) repairIntent(ctx context.Context, tx *canon.Tx, id Identity, linkIDs, dropLinks, drops []string, requeued bool, reason string, at time.Time, question bool) ([]byte, error) {
	intent := repairIntent{Version: 1}
	seen := map[string]bool{}
	for _, linkID := range linkIDs {
		if linkID == "" || seen[linkID] {
			continue
		}
		seen[linkID] = true
		link, err := readRepairLink(ctx, tx, linkID)
		if err != nil {
			return nil, err
		}
		intent.Links = append(intent.Links, link)
	}
	for _, item := range drops {
		if item == "" {
			continue
		}
		for _, linkID := range dropLinks {
			if linkID == "" {
				continue
			}
			intent.Drops = append(intent.Drops, repairDrop{Link: linkID, ID: item})
		}
	}
	if requeued {
		var row repairRequeue
		err := tx.QueryRowContext(ctx, `SELECT platform,scope,user,from_at,reason,queued_at FROM identity_requeue
WHERE platform=? AND scope=? AND user=? AND reason=? AND queued_at=?`,
			id.Platform, id.Scope, id.User, reason, formatTime(at)).Scan(&row.Platform, &row.Scope, &row.User, &row.From, &row.Reason, &row.At)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			intent.Requeues = append(intent.Requeues, row)
		}
	}
	if question {
		var row repairQuestion
		err := tx.QueryRowContext(ctx, `SELECT platform,scope,user,state FROM identity_question WHERE platform=? AND scope=? AND user=?`,
			id.Platform, id.Scope, id.User).Scan(&row.Platform, &row.Scope, &row.User, &row.State)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			intent.Question = &row
		}
	}
	if len(intent.Links) == 0 && len(intent.Drops) == 0 && len(intent.Requeues) == 0 && intent.Question == nil {
		return nil, nil
	}
	return json.Marshal(intent)
}

func readRepairLink(ctx context.Context, tx *canon.Tx, linkID string) (repairLink, error) {
	var link repairLink
	err := tx.QueryRowContext(ctx, `SELECT id,platform,scope,user,record,class,maker,created_at,revision,state,valid_from,valid_through
FROM identity_link WHERE id=?`, linkID).Scan(&link.ID, &link.Platform, &link.Scope, &link.User, &link.Record, &link.Class, &link.Maker, &link.CreatedAt, &link.Revision, &link.State, &link.From, &link.Through)
	if err != nil {
		return repairLink{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT action,actor,at FROM identity_link_change WHERE link=? AND revision=?`, linkID, link.Revision).Scan(&link.Action, &link.Actor, &link.At)
	return link, err
}

// ReplayRepair writes a journaled link repair. Core runs it while recovering
// the removal list, before reads reopen. A link already at the recorded
// revision is left as it is.
func ReplayRepair(ctx context.Context, tx *canon.Tx, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	var intent repairIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return err
	}
	if intent.Version != 1 {
		return ErrInvalid
	}
	for _, link := range intent.Links {
		if err := replayLink(ctx, tx, link); err != nil {
			return err
		}
	}
	for _, drop := range intent.Drops {
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_artifact WHERE link=? AND kind='item' AND id=?`, drop.Link, drop.ID); err != nil {
			return err
		}
	}
	for _, row := range intent.Requeues {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_requeue(platform,scope,user,from_at,reason,queued_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(platform,scope,user,from_at) DO NOTHING`, row.Platform, row.Scope, row.User, row.From, row.Reason, row.At); err != nil {
			return err
		}
	}
	if intent.Question != nil {
		row := intent.Question
		result, err := tx.ExecContext(ctx, `UPDATE identity_question SET state=? WHERE platform=? AND scope=? AND user=?`,
			row.State, row.Platform, row.Scope, row.User)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO identity_question(platform,scope,user,reason,asked_at,state,answer) VALUES(?,?,?,'','',?,'')
ON CONFLICT(platform,scope,user) DO UPDATE SET state=excluded.state`,
				row.Platform, row.Scope, row.User, row.State); err != nil {
				return err
			}
		}
	}
	return nil
}

func replayLink(ctx context.Context, tx *canon.Tx, link repairLink) error {
	if link.ID == "" || link.Revision < 1 || link.Action == "" {
		return ErrInvalid
	}
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT revision FROM identity_link WHERE id=?`, link.ID).Scan(&revision)
	write := false
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_link(id,platform,scope,user,record,class,maker,created_at,revision,state,valid_from,valid_through)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			link.ID, link.Platform, link.Scope, link.User, link.Record, link.Class, link.Maker, link.CreatedAt, link.Revision, link.State, link.From, link.Through); err != nil {
			return err
		}
		write = true
	} else if err != nil {
		return err
	} else if revision < link.Revision {
		result, err := tx.ExecContext(ctx, `UPDATE identity_link SET record=?,class=?,maker=?,revision=?,state=?,valid_from=?,valid_through=? WHERE id=? AND revision=?`,
			link.Record, link.Class, link.Maker, link.Revision, link.State, link.From, link.Through, link.ID, revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrInvalid
		}
		write = true
	}
	if write {
		if _, err := tx.ExecContext(ctx, `INSERT INTO identity_link_change(link,revision,action,class,state,record,actor,at) VALUES(?,?,?,?,?,?,?,?)`,
			link.ID, link.Revision, link.Action, link.Class, link.State, link.Record, link.Actor, link.At); err != nil {
			return err
		}
	}
	return nil
}
