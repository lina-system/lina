// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

var technicalStepName = regexp.MustCompile(`(?i)([#＃]\s*\p{N}+|\b(?:issue|pr|pull request)\s*[-:#]?\s*\d+|\b[a-z]+-\d+\b|\b[0-9a-f]{7,}\b|\b[\w-]+\.[a-z][\w]*\b|\b(?:branch|id)\s*[:=]|[_/\\` + "`" + `])`)

func validStepName(name string, forbidden []string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || uniseg.StringWidth(name) > 24 || technicalStepName.MatchString(name) {
		return false
	}
	for _, r := range name {
		// A joiner is part of emoji graphemes. Other invisible formatting/control
		// characters cannot hide technical tokens or alter terminal rendering.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != '\u200d' {
			return false
		}
	}
	for _, token := range forbidden {
		if token == "" {
			continue
		}
		pattern := `(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(token) + `($|[^\p{L}\p{N}])`
		if regexp.MustCompile(pattern).MatchString(name) {
			return false
		}
	}
	return uniseg.StringWidth(name) > 0
}
