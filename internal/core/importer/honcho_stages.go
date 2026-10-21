// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// This internal adapter seam is deliberately not a public way around the
// unregistered Honcho API gate. A future registered page decoder supplies it.
// Tests supply synthetic rows; no live endpoint or schema is invented here.
type honchoWorkspace struct {
	profile, workspace string
	messages, history  []Row
}

func (b *profileBuilder) addHoncho(w honchoWorkspace) error {
	if w.workspace == "" {
		return ErrSnapshot
	}
	for _, row := range w.messages {
		if err := checkRow(row); err != nil {
			return err
		}
		id, session, role := row.text("id"), row.text("session_id"), row.text("role")
		if id == "" || session == "" {
			return ErrSnapshot
		}
		var timed, exact []*record
		text := row.text("content")
		for _, c := range b.conversations {
			for _, r := range c.records {
				if r.event == nil || r.stage == 6 {
					continue
				}
				matchesTime, matchesText := false, false
				for _, s := range r.sources {
					if s.key.Kind != "messages" || s.key.System == "honcho" || s.row.text("session_id") != session || s.row.text("role") != role {
						continue
					}
					content := s.row.text("content")
					if text != "" && content == text {
						matchesText = true
					}
					if text != "" && content != "" && (strings.HasPrefix(content, text) || strings.HasPrefix(text, content)) && math.Abs(row.number("timestamp")-s.row.number("timestamp")) <= 60 {
						matchesTime = true
					}
				}
				if matchesTime {
					timed = append(timed, r)
				}
				if matchesText {
					exact = append(exact, r)
				}
			}
		}

		var target *record
		if len(timed) == 1 {
			target = timed[0]
		} else if len(timed) == 0 && len(exact) == 1 {
			target = exact[0]
		}
		s := sourceRow{key: LegacyKey{"honcho", w.workspace, "messages", id}, row: row}
		if target == nil {
			c := b.sessionConversations[session]
			if c == nil {
				c = b.conversation("honcho:" + w.workspace + ":" + session)
				b.sessionConversations[session] = c
			}
			target = b.message(c, s, 6)
			target.flags = []string{"honcho-only"}
			target.event.quality = "recorded"
		} else {
			b.attach(target, s)
		}
		target.event.honchoOwner = target.event.honchoOwner || row.text("owner_claim") == "true"
	}
	deleted := map[string]*record{}
	// Histories never enter the live summary tree, memory or recall tables.
	for _, row := range w.history {
		kind := row.text("kind")
		switch kind {
		case "workspace", "peer", "session", "session_summary", "conclusion", "card", "review_log":
		default:
			return fmt.Errorf("%w: Honcho record kind", ErrSnapshot)
		}
		id := row.text("id")
		if id == "" {
			return ErrSnapshot
		}
		c := b.sessionConversations[row.text("session_id")]
		if c == nil {
			c = b.conversation("honcho-history:" + w.workspace)
		}
		r := b.add(c, kind, 8, sourceRow{key: LegacyKey{"honcho", w.workspace, kind, id}, row: row})
		r.body = row.text("content")
		if row["content"].Kind == "text" {
			r.sources[0].bodyColumn = "content"
		}
		r.flags = []string{"legacy", "unverified"}
		if kind == "conclusion" {
			r.flags = append(r.flags, "group:"+digest(strings.Join(strings.Fields(r.body), " ")))
		}
		if row.text("deleted") == "true" {
			r.flags = append(r.flags, "retracted")
			if kind == "review_log" && row.text("conclusion_id") != "" {
				r.kind = "observation_tombstone"
				deleted[row.text("conclusion_id")] = r
			}
		}
	}
	for _, row := range w.history {
		r := b.records[LegacyKey{"honcho", w.workspace, row.text("kind"), row.text("id")}]
		for _, field := range []string{"source_ids", "supersedes"} {
			if row.text(field) == "" {
				continue
			}
			var ids []string
			if json.Unmarshal([]byte(row.text(field)), &ids) != nil {
				return ErrSnapshot
			}
			kind, relation := "messages", "source"
			if field == "supersedes" {
				kind, relation = "conclusion", "supersedes"
			}
			for i, id := range ids {
				target := b.records[LegacyKey{"honcho", w.workspace, kind, id}]
				if target == nil && kind == "conclusion" {
					target = deleted[id]
				}
				if target == nil {
					return ErrSourceMissing
				}
				r.links = append(r.links, importLink{target.id, relation, i})
			}
		}
	}
	counts := map[string]int{"messages": len(w.messages)}
	for _, row := range w.history {
		counts[row.text("kind")]++
	}
	for kind, n := range counts {
		b.p.inventory = append(b.p.inventory, TableCount{System: "honcho", Instance: w.workspace, Table: kind, Rows: n})
	}
	return nil
}
