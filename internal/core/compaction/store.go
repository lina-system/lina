// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type Store struct{ db *canon.Store }

func New(db *canon.Store) *Store { return &Store{db: db} }

func (s *Store) Frontier(ctx context.Context, conversation string) (Frontier, error) {
	var f Frontier
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; f, err = readFrontier(ctx, tx, conversation); return err })
	return f, err
}
func readFrontier(ctx context.Context, tx *canon.Tx, conversation string) (Frontier, error) {
	f := Frontier{Conversation: conversation}
	if strings.TrimSpace(conversation) == "" {
		return Frontier{}, ErrInvalid
	}
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&f.Epoch); err != nil {
		return Frontier{}, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM conv_event WHERE conversation_id=?", conversation).Scan(&f.Events); err != nil {
		return Frontier{}, err
	}
	if f.Events == 0 {
		return Frontier{}, ErrCoverage
	}
	if err := tx.QueryRowContext(ctx, "SELECT id FROM conv_event WHERE conversation_id=? ORDER BY rowid DESC LIMIT 1", conversation).Scan(&f.LastEvent); err != nil {
		return Frontier{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,revision FROM sum_node n WHERE conversation_id=? AND NOT EXISTS(SELECT 1 FROM sum_edge e WHERE e.child_id=n.id)`, conversation)
	if err != nil {
		return Frontier{}, err
	}
	defer rows.Close()
	for rows.Next() {
		if f.Root != "" {
			return Frontier{}, ErrCoverage
		}
		if err := rows.Scan(&f.Root, &f.Revision); err != nil {
			return Frontier{}, err
		}
	}
	return f, rows.Err()
}
func checkFrontier(ctx context.Context, tx *canon.Tx, expected Frontier) error {
	current, err := readFrontier(ctx, tx, expected.Conversation)
	if err != nil {
		return err
	}
	if current.Epoch != expected.Epoch {
		return ErrStaleEpoch
	}
	if current != expected {
		return ErrStaleFrontier
	}
	return nil
}

// Node returns derived content only at the current revocation epoch. It is an
// internal storage read; the consuming flow must authorize its conversation.
func (s *Store) Node(ctx context.Context, id string) (Node, error) {
	var n Node
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		n, err = readNode(ctx, tx, id)
		if err != nil {
			return err
		}
		var epoch uint64
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch); err != nil {
			return err
		}
		if n.Status != "ready" || n.Epoch != epoch {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return n, nil
}
func readNode(ctx context.Context, tx *canon.Tx, id string) (Node, error) {
	var n Node
	var built, start, end string
	err := tx.QueryRowContext(ctx, `SELECT id,conversation_id,level,model,prompt_version,built_at,start_at,end_at,epoch,status,text,dropped,revision FROM sum_node WHERE id=?`, id).Scan(&n.ID, &n.Conversation, &n.Level, &n.Output.Model, &n.Output.PromptVersion, &built, &start, &end, &n.Epoch, &n.Status, &n.Output.Text, &n.Output.Dropped, &n.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrUnavailable
	}
	if err != nil {
		return Node{}, err
	}
	for _, v := range []struct {
		text string
		dst  *time.Time
	}{{built, &n.Output.BuiltAt}, {start, &n.StartAt}, {end, &n.EndAt}} {
		*v.dst, err = time.Parse(time.RFC3339Nano, v.text)
		if err != nil {
			return Node{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT child_id FROM sum_edge WHERE parent_id=? ORDER BY ordinal", id)
	if err != nil {
		return Node{}, err
	}
	for rows.Next() {
		var child string
		if err := rows.Scan(&child); err != nil {
			rows.Close()
			return Node{}, err
		}
		n.Children = append(n.Children, child)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Node{}, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT event_id,reason FROM sum_cover WHERE node_id=? ORDER BY ordinal", id)
	if err != nil {
		return Node{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Coverage
		if err := rows.Scan(&c.EventID, &c.Reason); err != nil {
			return Node{}, err
		}
		n.Cover = append(n.Cover, c)
	}
	return n, rows.Err()
}

type event struct {
	id, replay, scaffold string
	at                   time.Time
	visible              bool
}

func events(ctx context.Context, tx *canon.Tx, conversation string) ([]event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.occurred_at,coalesce(e.replay_of,''),coalesce(e.scaffold_kind,''),EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=e.id) FROM conv_event e WHERE e.conversation_id=? ORDER BY e.rowid`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var e event
		var at string
		if err := rows.Scan(&e.id, &at, &e.replay, &e.scaffold, &e.visible); err != nil {
			return nil, err
		}
		e.at, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func validReason(c Coverage, e event) bool {
	if !e.visible {
		return c.Reason == "retracted"
	}
	switch c.Reason {
	case "":
		return true
	case "replay":
		return e.replay != ""
	case "scaffold":
		return e.scaffold != ""
	case "summary_echo":
		return e.scaffold == "compaction_recorded"
	default:
		return false
	}
}

// skipReason reconstructs canonical exclusions without treating provider items
// (native conversation content) as scaffolding.
func skipReason(e event) string {
	switch {
	case !e.visible:
		return "retracted"
	case e.replay != "":
		return "replay"
	case e.scaffold == "compaction_recorded":
		return "summary_echo"
	case e.scaffold != "" && e.scaffold != "provider_item":
		return "scaffold"
	default:
		return ""
	}
}
func validOutput(o Output) bool {
	if strings.TrimSpace(o.Text) == "" || strings.TrimSpace(o.Model) == "" || strings.TrimSpace(o.PromptVersion) == "" || o.BuiltAt.IsZero() {
		return false
	}
	for _, text := range []string{o.Text, o.Dropped, o.Model, o.PromptVersion} {
		masked, err := secrets.Mask(text)
		if err != nil || masked.Text() != text {
			return false
		}
	}
	return true
}
