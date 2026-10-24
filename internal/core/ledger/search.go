// SPDX-License-Identifier: Apache-2.0
package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type SearchQuery struct {
	AgentID, PersonID, Text string
	Limit                   int
	Origin                  string
	SpeakerKind             guard.SpeakerKind
	Since, Until            time.Time
	IncludeReplay           bool
}

// SearchHit carries source identity and provenance with the secret-free text.
// It is evidence of a past event, never a model message or current authority.
type SearchHit struct {
	ID, ConversationID, AgentID string
	Body                        string
	Origin                      Origin
	SpeakerKind                 guard.SpeakerKind
	OccurredAt                  time.Time
}

func indexText(ctx context.Context, tx *canon.Tx, id, text string) error {
	for _, table := range []string{"ledger_search_text", "ledger_search_bigram"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE event_id=?", id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO ledger_search_text(event_id,body) VALUES(?,?)", id, text); err != nil {
		return err
	}
	for _, gram := range canon.HangulBigrams(text) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO ledger_search_bigram(gram,event_id) VALUES(?,?)", gram, id); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM ledger_search_pending WHERE event_id=?", id)
	return err
}

// Search is the ledger view of the one recall entry. Scope, matching and
// ranking come from recall; this function keeps the ledger's source identity
// and secret-free derived text.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if !safeID(q.AgentID) || !safeID(q.PersonID) || !utf8.ValidString(q.Text) || strings.ContainsRune(q.Text, 0) || q.Limit < 0 || q.Limit > 100 {
		return nil, ErrInvalid
	}
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, nil
	}
	hits, err := recall.Match(ctx, s.db, recall.Scope{
		Person: q.PersonID, Agent: q.AgentID, Kinds: []recall.Kind{recall.Event},
		Since: q.Since, Until: q.Until, SpeakerKind: q.SpeakerKind, Origin: q.Origin, IncludeReplay: q.IncludeReplay,
	}, recall.Query{Text: text, Limit: q.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		source, err := s.searchSource(ctx, hit.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, source)
	}
	return out, nil
}

func (s *Store) searchSource(ctx context.Context, id string) (SearchHit, error) {
	var hit SearchHit
	var text, materialID sql.NullString
	var revision sql.NullInt64
	var spans []secrets.Span
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var origin, occurred string
		if err := tx.QueryRowContext(ctx, `SELECT id,conversation_id,agent_id,body,material_id,material_revision,origin,speaker_kind,occurred_at
		 FROM retract_visible_event WHERE id=?`, id).Scan(&hit.ID, &hit.ConversationID, &hit.AgentID, &text, &materialID, &revision, &origin, &hit.SpeakerKind, &occurred); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(origin), &hit.Origin); err != nil {
			return err
		}
		var err error
		if hit.OccurredAt, err = time.Parse(time.RFC3339Nano, occurred); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT start,end,sensitivity FROM ev_span WHERE event_id=? AND sensitivity='secret' ORDER BY start,end", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var span secrets.Span
			if err := rows.Scan(&span.Start, &span.End, &span.Sensitivity); err != nil {
				return err
			}
			spans = append(spans, span)
		}
		return rows.Err()
	})
	if err != nil {
		return SearchHit{}, err
	}
	if hit.Origin.Kind == "import" && materialID.Valid {
		event, err := s.Get(ctx, id)
		if err != nil {
			return SearchHit{}, err
		}
		hit.Body = event.Body.DerivedText()
		return hit, nil
	}
	if hit.Origin.Kind == "import" {
		_, missing, err := s.importedOutput(ctx, id, text.String)
		if err != nil {
			return SearchHit{}, err
		}
		if missing {
			return SearchHit{}, ErrBytesUnavailable
		}
	}
	if materialID.Valid {
		if s.materials == nil {
			return SearchHit{}, ErrBytesUnavailable
		}
		text.String, err = s.materials.Read(ctx, Material{ID: materialID.String, Revision: revision.Int64})
		if err != nil {
			return SearchHit{}, errors.Join(ErrBytesUnavailable, err)
		}
	}
	masked, err := secrets.Mask(text.String, spans...)
	if err != nil {
		return SearchHit{}, err
	}
	hit.Body = masked.DerivedText()
	return hit, nil
}
