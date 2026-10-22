// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// Shape retains paragraph separators and Unicode verbatim. Oversized individual
// paragraphs split only at a rune boundary. The queue receives at most three parts.
func Shape(text, summary, link string) ([]string, error) {
	if text == "" || !utf8.ValidString(text) {
		return nil, errTooLong
	}
	var parts []string
	for remaining := text; remaining != ""; {
		end, n := 0, 0
		for i, r := range remaining {
			if n+utf16.RuneLen(r) > 2000 {
				break
			}
			n += utf16.RuneLen(r)
			end = i + utf8.RuneLen(r)
		}
		if end < len(remaining) {
			if i := strings.LastIndex(remaining[:end], "\n\n"); i >= 0 {
				end = i + 2
			}
		}
		parts = append(parts, remaining[:end])
		remaining = remaining[end:]
		if len(parts) > 3 {
			if strings.TrimSpace(summary) == "" || strings.TrimSpace(link) == "" || units(summary+"\n"+link) > 2000 {
				return nil, errTooLong
			}
			return []string{summary + "\n" + link}, nil
		}
	}
	return parts, nil
}
