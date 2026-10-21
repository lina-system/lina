// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/ncruces/go-sqlite3"
)

// SQLite triggers and migration backfills share the same extractor as native
// ledger admission. Go walks the text once; a recursive substr() SQL loop would
// repeatedly traverse a long UTF-8 value for every syllable pair.
func registerSearchFunctions(conn *sqlite3.Conn) error {
	return conn.CreateFunction("lina_hangul_bigrams", 1, sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS, func(ctx sqlite3.Context, args ...sqlite3.Value) {
		pairs := HangulBigrams(args[0].Text())
		if len(pairs) == 0 {
			ctx.ResultText("[]")
			return
		}
		data, err := json.Marshal(pairs)
		if err != nil {
			ctx.ResultError(err)
			return
		}
		ctx.ResultText(string(data))
	})
}

// HangulBigrams returns distinct adjacent pairs of composed Hangul syllables
// in stable order. Spaces, punctuation and other scripts separate runs: index
// projections must preserve such boundaries when removing secret spans.
func HangulBigrams(text string) []string {
	seen := make(map[string]bool)
	var previous rune
	for _, current := range text {
		if hangulSyllable(previous) && hangulSyllable(current) {
			seen[string([]rune{previous, current})] = true
		}
		previous = current
	}
	var pairs []string
	for pair := range seen {
		pairs = append(pairs, pair)
	}
	sort.Strings(pairs)
	return pairs
}

func hangulSyllable(r rune) bool { return r >= '\uac00' && r <= '\ud7a3' }

// TrigramPhrase quotes a literal substring for FTS5 MATCH. In particular, a
// person's quotes, operators and punctuation cannot become query syntax.
// FTS5's trigram tokenizer returns no hits for fewer than three characters;
// callers use the Hangul bigram index for two-syllable queries.
func TrigramPhrase(text string) string {
	return `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
}
