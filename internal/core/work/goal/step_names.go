// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

var technicalStepName = regexp.MustCompile(`(?i)([#＃]\s*\p{N}+|(?:\b(?:issue|pr|pull request)|이슈|풀\s*리퀘스트)\s*[-:#]?\s*\p{N}+|\b[a-z]+-\p{N}+\b|\b[0-9a-f]{7,}\b|(?:^|[^\p{L}\p{N}])[\p{L}\p{N}_-]+\.[\p{L}][\p{L}\p{N}_-]*|\b(?:branch|id)\s*[:=]|[_/\\` + "`" + `])`)

func validStepName(name string, forbidden []string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || uniseg.StringWidth(name) > 24 {
		return false
	}
	for _, r := range name {
		// A joiner is part of emoji graphemes. Other invisible formatting/control
		// characters cannot hide technical tokens or alter terminal rendering.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '\u200d' {
			return false
		}
	}
	// Emoji joiners/selectors affect display width, but must not conceal a
	// technical token. Keep the saved spelling and inspect a visible-token copy.
	visible := strings.Map(func(r rune) rune {
		if r == '\u200d' || r == '\u034f' || r >= '\ufe00' && r <= '\ufe0f' || r >= '\U000e0100' && r <= '\U000e01ef' {
			return -1
		}
		return r
	}, name)
	if technicalStepName.MatchString(visible) {
		return false
	}
	for _, token := range forbidden {
		if token == "" {
			continue
		}
		pattern := `(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(token) + `($|[^\p{L}\p{N}])`
		if regexp.MustCompile(pattern).MatchString(visible) {
			return false
		}
	}
	return uniseg.StringWidth(name) > 0
}
