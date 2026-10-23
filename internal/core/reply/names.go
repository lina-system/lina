// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"golang.org/x/text/unicode/norm"
)

// UsesRetiredName compares NFC/case-folded literal names, with Latin identifier
// boundaries. Hangul particles may follow a Korean name. Current full names are
// excluded before matching aliases, including a name that extends its old one.
func UsesRetiredName(text string, names []memory.Item) bool {
	fold := func(s string) string { return strings.ToLower(norm.NFC.String(s)) }
	text = fold(text)
	for _, item := range names {
		if item.Name == nil {
			continue
		}
		candidate := text
		current := fold(item.Name.Current)
		for start := 0; current != "" && start < len(candidate); {
			n := strings.Index(candidate[start:], current)
			if n < 0 {
				break
			}
			n += start
			end := n + len(current)
			if nameBoundary(candidate, n, end) {
				candidate = candidate[:n] + "\x00" + candidate[end:]
				start = n + 1
			} else {
				start = end
			}
		}
		for _, alias := range item.Name.RetiredAliases {
			alias = fold(alias)
			if alias == "" {
				continue
			}
			for start := 0; start < len(candidate); {
				n := strings.Index(candidate[start:], alias)
				if n < 0 {
					break
				}
				n += start
				end := n + len(alias)
				if nameBoundary(candidate, n, end) {
					return true
				}
				start = end
			}
		}
	}
	return false
}

func nameBoundary(text string, start, end int) bool {
	first, _ := utf8.DecodeRuneInString(text[start:end])
	last, _ := utf8.DecodeLastRuneInString(text[start:end])
	before, _ := utf8.DecodeLastRuneInString(text[:start])
	after, _ := utf8.DecodeRuneInString(text[end:])
	word := func(r rune) bool { return unicode.Is(unicode.Latin, r) || unicode.IsDigit(r) || r == '_' || r == '-' }
	return !(word(first) && word(before)) && !(word(last) && word(after))
}

// RecordNamingBreach shares the reply's admission transaction. The primary key
// makes completion retries count once, with no copy of names or response text.
func RecordNamingBreach(ctx context.Context, tx *canon.Tx, person, agent, event, text string, names []memory.Item) error {
	if !UsesRetiredName(text, names) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO naming_breaches(event_id,person,agent) VALUES(?,?,?) ON CONFLICT(event_id) DO NOTHING`, event, person, agent)
	return err
}

func NamingBreaches(ctx context.Context, db *canon.Store, person, agent string) (int64, error) {
	var count int64
	err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM naming_breaches WHERE person=? AND agent=?`, person, agent).Scan(&count)
	})
	return count, err
}
