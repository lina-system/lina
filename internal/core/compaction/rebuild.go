// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Rebuild regenerates stale nodes bottom-up, excluding revoked evidence and
// never using invalidated child text. Model work runs outside transactions. All
// replacements commit together, or none do if a model fails or the frontier or
// epoch changes. Node identities and their single-parent lineage are preserved.
func (s *Store) Rebuild(ctx context.Context, conversation string, summarize Summarizer) (Node, error) {
	if summarize == nil {
		return Node{}, ErrInvalid
	}
	var expected Frontier
	nodes := map[string]Node{}
	var order []string
	visible := map[string]bool{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		expected, err = readFrontier(ctx, tx, conversation)
		if err != nil {
			return err
		}
		if expected.Root == "" {
			return ErrUnavailable
		}
		all, err := events(ctx, tx, conversation)
		if err != nil {
			return err
		}
		for _, e := range all {
			visible[e.id] = e.visible
		}
		var visit func(string) error
		visit = func(id string) error {
			if _, seen := nodes[id]; seen {
				return ErrCoverage
			}
			n, err := readNode(ctx, tx, id)
			if err != nil {
				return err
			}
			if n.Status == "degraded" {
				return ErrUnavailable
			}
			nodes[id] = n
			for _, id := range n.Children {
				if err := visit(id); err != nil {
					return err
				}
			}
			order = append(order, id)
			return nil
		}
		return visit(expected.Root)
	})
	if err != nil {
		return Node{}, err
	}
	var changed []string
	for _, id := range order {
		n := nodes[id]
		if n.Epoch == expected.Epoch && n.Status == "ready" {
			continue
		}
		in := Segment{NodeID: id}
		for i, c := range n.Cover {
			if !visible[c.EventID] {
				n.Cover[i].Reason = "retracted"
				continue
			}
			if c.Reason == "retracted" {
				n.Cover[i].Reason = ""
			}
			if n.Cover[i].Reason == "" {
				in.Events = append(in.Events, c.EventID)
			}
		}
		for _, id := range n.Children {
			child := nodes[id]
			child.Cover = slices.Clone(child.Cover)
			child.Children = slices.Clone(child.Children)
			in.Children = append(in.Children, child)
		}
		if err := ctx.Err(); err != nil {
			return Node{}, err
		}
		out, err := summarize(ctx, in)
		if err != nil {
			return Node{}, err
		}
		if !validOutput(out) {
			return Node{}, ErrInvalid
		}
		out.BuiltAt = out.BuiltAt.UTC()
		n.Output = out
		n.Epoch = expected.Epoch
		n.Status = "ready"
		n.Revision++
		nodes[id] = n
		changed = append(changed, id)
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := checkFrontier(ctx, tx, expected); err != nil {
			return err
		}
		for _, id := range changed {
			n := nodes[id]
			if _, err := tx.ExecContext(ctx, `UPDATE sum_node SET model=?,prompt_version=?,built_at=?,epoch=?,status='ready',text=?,dropped=?,revision=? WHERE id=?`, n.Output.Model, n.Output.PromptVersion, stamp(n.Output.BuiltAt), n.Epoch, n.Output.Text, n.Output.Dropped, n.Revision, id); err != nil {
				return err
			}
			for _, c := range n.Cover {
				if _, err := tx.ExecContext(ctx, "UPDATE sum_cover SET reason=? WHERE node_id=? AND event_id=?", c.Reason, id, c.EventID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return nodes[expected.Root], nil
}
