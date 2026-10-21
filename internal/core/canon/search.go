// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"sort"
	"strings"
)

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
