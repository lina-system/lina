// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Publish commits node, links and exact event coverage together, after checking
// the preparation's ledger/root frontier and revocation epoch inside the writer.
// Verbatim names events retained alongside the root in the request, not inputs
// to this summary. A failed publication returns no usable node.
func (s *Store) Publish(ctx context.Context, p Proposal) (Node, error) {
	return s.publish(ctx, p, nil)
}

func (s *Store) publish(ctx context.Context, p Proposal, audit func(*canon.Tx) error) (Node, error) {
	if strings.TrimSpace(p.ID) == "" || !validOutput(p.Output) {
		return Node{}, ErrInvalid
	}
	var n Node
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := checkFrontier(ctx, tx, p.Expected); err != nil {
			return err
		}
		var err error
		n, err = validateCoverage(ctx, tx, p)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sum_node(id,conversation_id,level,model,prompt_version,built_at,start_at,end_at,epoch,status,text,dropped,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, n.ID, n.Conversation, n.Level, n.Output.Model, n.Output.PromptVersion, stamp(n.Output.BuiltAt), stamp(n.StartAt), stamp(n.EndAt), n.Epoch, n.Status, n.Output.Text, n.Output.Dropped, n.Revision)
		if err != nil {
			return err
		}
		for i, id := range n.Children {
			if _, err := tx.ExecContext(ctx, "INSERT INTO sum_edge(parent_id,child_id,ordinal) VALUES(?,?,?)", n.ID, id, i); err != nil {
				return err
			}
		}
		for i, c := range n.Cover {
			if _, err := tx.ExecContext(ctx, "INSERT INTO sum_cover(node_id,event_id,ordinal,reason) VALUES(?,?,?,?)", n.ID, c.EventID, i, c.Reason); err != nil {
				return err
			}
		}
		if audit != nil {
			return audit(tx)
		}
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return n, nil
}
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func validateCoverage(ctx context.Context, tx *canon.Tx, p Proposal) (Node, error) {
	n := Node{ID: p.ID, Conversation: p.Expected.Conversation, Epoch: p.Expected.Epoch, Revision: 1, Status: "ready", Output: p.Output, Children: slices.Clone(p.Children), Cover: slices.Clone(p.Cover)}
	n.Output.BuiltAt = n.Output.BuiltAt.UTC()
	// A compaction extends the current root; it cannot fork or reuse an interior
	// node and thereby assign the same event range to two parents.
	if p.Expected.Root == "" && len(p.Children) != 0 || p.Expected.Root != "" && !slices.Equal(p.Children, []string{p.Expected.Root}) {
		return Node{}, ErrCoverage
	}
	all, err := events(ctx, tx, n.Conversation)
	if err != nil {
		return Node{}, err
	}
	covered := map[string]bool{}
	add := func(c Coverage) error {
		// Children, then direct coverage, then the verbatim tail must consume
		// the ledger in append order. A complete set alone permits interleaving
		// a newer summarized event ahead of an older retained event.
		if len(covered) >= len(all) || all[len(covered)].id != c.EventID || !validReason(c, all[len(covered)]) {
			return ErrCoverage
		}
		covered[c.EventID] = true
		return nil
	}
	var walk func(string) error
	walk = func(id string) error {
		child, err := readNode(ctx, tx, id)
		if err != nil {
			return err
		}
		if child.Conversation != n.Conversation || child.Status != "ready" || child.Epoch != n.Epoch {
			return ErrUnavailable
		}
		for _, id := range child.Children {
			if err := walk(id); err != nil {
				return err
			}
		}
		for _, c := range child.Cover {
			if err := add(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range p.Children {
		child, err := readNode(ctx, tx, id)
		if err != nil {
			return Node{}, err
		}
		n.Level = max(n.Level, child.Level+1)
		if err := walk(id); err != nil {
			return Node{}, err
		}
	}
	for _, c := range p.Cover {
		if err := add(c); err != nil {
			return Node{}, err
		}
	}
	if len(covered) == 0 {
		return Node{}, ErrCoverage
	}
	for _, e := range all {
		if covered[e.id] {
			if n.StartAt.IsZero() || e.at.Before(n.StartAt) {
				n.StartAt = e.at
			}
			if n.EndAt.IsZero() || e.at.After(n.EndAt) {
				n.EndAt = e.at
			}
		}
	}
	// The remaining ledger suffix may contain excluded control/replay events
	// between retained messages. These are not owned by the summary, and cannot
	// be used to smuggle ordinary input past the prefix boundary.
	verbatim, skipped := 0, 0
	for _, e := range all[len(covered):] {
		c := Coverage{EventID: e.id}
		if skipped < len(p.TailSkips) && p.TailSkips[skipped].EventID == e.id {
			c = p.TailSkips[skipped]
			if c.Reason == "" || skipReason(e) == "" {
				return Node{}, ErrCoverage
			}
			skipped++
		} else if verbatim < len(p.Verbatim) && p.Verbatim[verbatim] == e.id {
			verbatim++
		} else {
			return Node{}, ErrCoverage
		}
		if err := add(c); err != nil {
			return Node{}, err
		}
	}
	if verbatim != len(p.Verbatim) || skipped != len(p.TailSkips) {
		return Node{}, ErrCoverage
	}
	return n, nil
}
