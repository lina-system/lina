// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"regexp"
	"strings"
)

// ErrPersonLine rejects a start line or summary that names an id, an issue or
// pull request number, a branch, a path or a check name.
var ErrPersonLine = errors.New("person_line_rejected")

// personTechnical matches the tokens a person-facing line must not carry.
// A dotted word is a path or a check name. A slash, backslash or backtick is a
// path. issue/PR numbers, hex ids and branch or id labels are the rest.
var personTechnical = regexp.MustCompile(`(?i)(?:[#＃]\s*\p{N}+|(?:\b(?:issue|pr|pull request)|이슈|풀\s*리퀘스트)\s*[-:#]?\s*\p{N}+|\b[\p{L}][\p{L}\p{N}]*(?:-[\p{L}\p{N}]+)+\b|\b[0-9a-f]{7,}\b|(?:^|[^\p{L}\p{N}])[\p{L}\p{N}_-]+\.[\p{L}][\p{L}\p{N}_-]*|\b(?:branch|id)\s*[:=]|[_/\\` + "`" + `])`)

// RejectPersonLine accepts one plain line. forbidden holds check names and
// other tokens that are technical even when they are ordinary words.
// The check lives with work so a goal can use it without importing a higher layer.
func RejectPersonLine(text string, forbidden []string) error {
	if strings.TrimSpace(text) == "" || strings.ContainsAny(text, "\r\n") || personTechnical.MatchString(text) {
		return ErrPersonLine
	}
	for _, token := range forbidden {
		token = strings.TrimSpace(token)
		if len([]rune(token)) < 2 {
			continue
		}
		pattern := `(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(token) + `($|[^\p{L}\p{N}])`
		if regexp.MustCompile(pattern).MatchString(text) {
			return ErrPersonLine
		}
	}
	return nil
}
