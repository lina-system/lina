// SPDX-License-Identifier: Apache-2.0

// Package recall maintains the shared search index beside the existing readers.
// Only Core writers call it; scope and source revisions always come from canon.
package recall

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

type source struct {
	kind, id, revision, person, holder, conversation, occurred, speaker, origin string
	replay                                                                      bool
	body                                                                        sql.NullString
}

func readSource(ctx context.Context, tx *canon.Tx, kind, id string) (source, error) {
	var s source
	err := tx.QueryRowContext(ctx, `SELECT kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay,body FROM recall_source WHERE kind=? AND source_id=?`, kind, id).Scan(&s.kind, &s.id, &s.revision, &s.person, &s.holder, &s.conversation, &s.occurred, &s.speaker, &s.origin, &s.replay, &s.body)
	return s, err
}

// Index commits a source's already derived, secret-free text in its writer's
// transaction. It rechecks visibility and reads metadata from the current source.
// Callers must finish source, span and citation writes before calling Index.
func Index(ctx context.Context, tx *canon.Tx, kind, id, text string) error {
	s, err := readSource(ctx, tx, kind, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, e := tx.ExecContext(ctx, "DELETE FROM recall_doc WHERE kind=? AND source_id=?", kind, id); e != nil {
		return e
	}
	if err == nil {
		result, e := tx.ExecContext(ctx, `INSERT INTO recall_doc(kind,source_id,revision,person,holder,conversation,occurred_at,speaker_kind,origin,replay) VALUES(?,?,?,?,?,?,?,?,?,?)`, s.kind, s.id, s.revision, s.person, s.holder, s.conversation, s.occurred, s.speaker, s.origin, s.replay)
		if e != nil {
			return e
		}
		row, e := result.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO recall_text(rowid,body) VALUES(?,?)", row, text); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO recall_pair(rowid,body) VALUES(?,?)", row, PairTokens(text)); e != nil {
			return e
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM recall_pending WHERE kind=? AND source_id=?", kind, id)
	return err
}

// IndexText derives text for writers holding an unprojected source body.
// Event writers instead pass their existing derived projection to Index.
func IndexText(ctx context.Context, tx *canon.Tx, kind, id, text string) error {
	masked, err := secrets.Mask(text)
	if err != nil {
		return err
	}
	return Index(ctx, tx, kind, id, masked.DerivedText())
}

// PairTokens preserves occurrence counts for BM25. Composed Hangul runs become
// overlapping pairs (one syllable stays whole); other scripts become lower-case
// words. Script changes and punctuation break runs, including masked spans.
func PairTokens(text string) string {
	var tokens []string
	var run []rune
	hangul := func(r rune) bool { return r >= '\uac00' && r <= '\ud7a3' }
	flush := func() {
		if len(run) == 0 {
			return
		}
		if hangul(run[0]) && len(run) > 1 {
			for i := 1; i < len(run); i++ {
				tokens = append(tokens, string(run[i-1:i+1]))
			}
		} else {
			tokens = append(tokens, strings.ToLower(string(run)))
		}
		run = run[:0]
	}
	for _, r := range text {
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r) {
			flush()
			continue
		}
		if len(run) > 0 && hangul(run[0]) != hangul(r) {
			flush()
		}
		run = append(run, r)
	}
	flush()
	return strings.Join(tokens, " ")
}
