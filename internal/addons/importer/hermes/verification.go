// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
)

// Source hashes alone do not authenticate the staged interpretation. Check
// attribution, ordering, flags and every derived edge before publication too.
func verifyInterpretation(ctx context.Context, tx *canon.Tx, c *conversation) error {
	type edge struct {
		from, to, kind string
		ordinal        int
	}
	edges := map[edge]bool{}
	count := func(query string, want int, args ...any) error {
		var n int
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			return err
		}
		if n != want {
			return ErrConflict
		}
		return nil
	}
	for _, r := range c.records {
		flags, _ := json.Marshal(r.flags)
		body := r.body
		eventID := ""
		if r.event != nil {
			body = ""
			eventID = r.id
		}
		if err := count(`SELECT count(*) FROM import_record WHERE id=? AND conversation_id=? AND agent_id=? AND kind=? AND stage=? AND body=? AND flags=? AND coalesce(event_id,'')=?`, 1, r.id, r.conversation, r.agent, r.kind, r.stage, body, string(flags), eventID); err != nil {
			return err
		}
		if err := count("SELECT count(*) FROM import_link WHERE record_id=?", len(r.links), r.id); err != nil {
			return err
		}
		for _, l := range r.links {
			if err := count("SELECT count(*) FROM import_link WHERE record_id=? AND target_id=? AND kind=? AND ordinal=?", 1, r.id, l.target, l.kind, l.ordinal); err != nil {
				return err
			}
			if r.event != nil && (l.kind == "view_of" || l.kind == "supersedes") {
				edges[edge{r.id, l.target, l.kind, l.ordinal}] = true
			}
		}
		if r.kind == "relation" && len(r.links) == 2 {
			a, b := r.links[0], r.links[1]
			edges[edge{a.target, b.target, b.kind, b.ordinal}] = true
		}
		if e := r.event; e != nil {
			if err := count(`SELECT count(*) FROM import_event WHERE id=? AND conversation_id=? AND coalesce(parent_id,'')=? AND coalesce(person_id,'')=? AND agent_id=? AND speaker_kind=? AND occurred_at=? AND recorded_at=? AND time_quality=? AND coalesce(replay_of,'')=? AND body=?`, 1, r.id, r.conversation, e.parent, e.classified.person, r.agent, e.classified.kind, e.occurred, e.recorded, e.quality, e.replay, r.body); err != nil {
				return err
			}
			spans := ScanSecrets([]byte(r.body))
			if err := count("SELECT count(*) FROM import_event_span WHERE event_id=?", len(spans), r.id); err != nil {
				return err
			}
			for _, s := range spans {
				if err := count("SELECT count(*) FROM import_event_span WHERE event_id=? AND start=? AND end=? AND sensitivity=?", 1, r.id, s.Start, s.End, s.Kind); err != nil {
					return err
				}
			}
		}
	}
	// Relations may be owned by a different conversation (a cross-range edge
	// represented by a source relation record). Check the expected edges here;
	// verifyEdges checks the complete set for missing and extra edges.
	for e := range edges {
		if err := count("SELECT count(*) FROM import_event_relation WHERE event_id=? AND target_id=? AND kind=? AND ordinal=?", 1, e.from, e.to, e.kind, e.ordinal); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) verifyEdges(ctx context.Context, tx *canon.Tx) error {
	type edge struct {
		from, to, kind string
		ordinal        int
	}
	expected := map[edge]bool{}
	for _, c := range p.conversations {
		for _, r := range c.records {
			if r.event != nil {
				for _, l := range r.links {
					if l.kind == "view_of" || l.kind == "supersedes" {
						expected[edge{r.id, l.target, l.kind, l.ordinal}] = true
					}
				}
			}
			if r.kind == "relation" && len(r.links) == 2 {
				a, b := r.links[0], r.links[1]
				expected[edge{a.target, b.target, b.kind, b.ordinal}] = true
			}
		}
	}
	seen := map[edge]bool{}
	for _, c := range p.conversations {
		rows, err := tx.QueryContext(ctx, `SELECT r.event_id,r.target_id,r.kind,r.ordinal FROM import_event_relation r JOIN import_event e ON e.id=r.event_id WHERE e.conversation_id=?`, c.record.id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e edge
			if err = rows.Scan(&e.from, &e.to, &e.kind, &e.ordinal); err != nil {
				rows.Close()
				return err
			}
			if !expected[e] {
				rows.Close()
				return ErrConflict
			}
			seen[e] = true
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	if len(seen) != len(expected) {
		return ErrConflict
	}
	return nil
}
