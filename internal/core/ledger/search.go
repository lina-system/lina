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

// The current ledger has no conversation-owner table. Require a person event
// proving ownership, and refuse ambiguous conversations. This also scopes
// agent/tool replies, whose own rows deliberately have no person_id.
const searchScope = `e.agent_id=?
 AND coalesce(e.scaffold_kind,'')<>'reply_check'
 AND NOT (coalesce(e.scaffold_kind,'')='result' AND EXISTS(SELECT 1 FROM ev_rel view WHERE view.event_id=e.id AND view.kind='view_of'))
 AND EXISTS(SELECT 1 FROM conv_event owner WHERE owner.conversation_id=e.conversation_id
  AND owner.agent_id=e.agent_id AND owner.speaker_kind='person' AND owner.person_id=?)
 AND NOT EXISTS(SELECT 1 FROM conv_event other WHERE other.conversation_id=e.conversation_id
  AND other.speaker_kind='person' AND other.person_id<>?)`

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

// Search uses trigram MATCH for literal substrings and postings for exactly two
// Hangul syllables. Shorter/non-Hangul two-character queries have no postings.
// Scope and filters constrain the result before its limit (default 20, max 100).
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]SearchHit, error) {
	if !safeID(q.AgentID) || !safeID(q.PersonID) || !utf8.ValidString(q.Text) || strings.ContainsRune(q.Text, 0) || q.Limit < 0 || q.Limit > 100 {
		return nil, ErrInvalid
	}
	text := strings.TrimSpace(q.Text)
	if utf8.RuneCountInString(text) < 3 && len(canon.HangulBigrams(text)) != 1 {
		return nil, nil
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if err := s.backfillSearch(ctx, q.AgentID, q.PersonID); err != nil {
		return nil, err
	}
	where := searchScope
	args := []any{q.AgentID, q.PersonID, q.PersonID}
	if utf8.RuneCountInString(text) == 2 {
		where += " AND EXISTS(SELECT 1 FROM ledger_search_bigram b WHERE b.event_id=e.id AND b.gram=?)"
		args = append(args, text)
	} else {
		where += " AND d.rowid IN (SELECT rowid FROM ledger_search WHERE ledger_search MATCH ?)"
		args = append(args, canon.TrigramPhrase(text))
	}
	if !q.IncludeReplay {
		where += " AND e.replay_of IS NULL"
	}
	if q.Origin != "" {
		where += " AND json_extract(e.origin,'$.kind')=?"
		args = append(args, q.Origin)
	}
	if q.SpeakerKind != "" {
		where += " AND e.speaker_kind=?"
		args = append(args, q.SpeakerKind)
	}
	if !q.Since.IsZero() {
		where += " AND julianday(e.occurred_at)>=julianday(?)"
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		where += " AND julianday(e.occurred_at)<julianday(?)"
		args = append(args, q.Until.UTC().Format(time.RFC3339Nano))
	}
	args = append(args, q.Limit)
	var ids []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT e.id FROM retract_visible_event e JOIN ledger_search_text d ON d.event_id=e.id WHERE "+where+" ORDER BY julianday(e.occurred_at) DESC,e.id LIMIT ?", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	var hits []SearchHit
	for _, id := range ids {
		hit, err := s.searchSource(ctx, id)
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Old and imported rows are queued durably by the migration/insert trigger.
// Material I/O is outside transactions. A failed read leaves the pending row
// intact and fails search, so a missing body cannot silently become no match.
func (s *Store) backfillSearch(ctx context.Context, agent, person string) error {
	for {
		var ids []string
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			rows, err := tx.QueryContext(ctx, `SELECT p.event_id FROM ledger_search_pending p
			 JOIN retract_visible_event e ON e.id=p.event_id WHERE `+searchScope+` ORDER BY p.event_id LIMIT 128`, agent, person, person)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil || len(ids) == 0 {
			return err
		}
		for _, id := range ids {
			hit, err := s.searchSource(ctx, id)
			if err != nil {
				return err
			}
			if err := s.db.Write(ctx, func(tx *canon.Tx) error { return indexText(ctx, tx, id, hit.Body) }); err != nil {
				return err
			}
		}
	}
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
