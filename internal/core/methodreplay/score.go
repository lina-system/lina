// SPDX-License-Identifier: Apache-2.0
package methodreplay

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The checks are deterministic text measures, not a judge. Each one restates a
// mechanical expectation of the design's measurement section.
func Score(c Case, reply string) Result {
	text := norm.NFC.String(reply)
	prose := proseLines(text)
	// Every check ignores fenced code blocks, including length: a code sample is
	// not the person's prose, so its characters do not spend the reply budget.
	result := Result{Chars: chars(strings.Join(prose, "\n")), Checks: make([]CheckResult, 0, len(c.Checks))}
	empty := strings.TrimSpace(text) == ""
	for _, check := range c.Checks {
		passed := false
		if !empty {
			switch check {
			case Length:
				passed = result.Chars <= c.Budget
			case NoQuestion:
				passed = !asks(prose)
			case NoInternal:
				passed = !internal(prose, c.RetiredAliases)
			case DoneEvidence:
				passed = doneEvidence(prose)
			case Language:
				passed = language(prose, c.Language)
			}
		}
		result.Checks = append(result.Checks, CheckResult{Check: check, Passed: passed})
	}
	all := len(c.Checks) > 0
	for _, check := range result.Checks {
		all = all && check.Passed
	}
	result.Passed = all
	return result
}

// chars counts non-space characters, the Korean unit reply budgets use.
func chars(text string) int {
	n := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// proseLines returns visible lines outside fenced code blocks. Code samples are
// not the person's prose, so questions, identifiers and language there do not
// count against the reply. The fence kept is the whole opening run, so a
// three-backtick line inside a four-backtick block does not end it.
func proseLines(text string) []string {
	var out []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
			}
			continue
		}
		if open, ok := openingFence(line); ok {
			fence = open
			continue
		}
		out = append(out, line)
	}
	return out
}

// openingFence returns the full run of backticks or tildes that opens a fence.
// A longer fence can contain a shorter one, so the run length is the close rule.
func openingFence(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 {
		return "", false
	}
	mark := trimmed[0]
	if mark != '`' && mark != '~' {
		return "", false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == mark {
		n++
	}
	if n < 3 {
		return "", false
	}
	return trimmed[:n], true
}

// closesFence reports a closing fence of at least the opening length whose line
// holds only that fence character.
func closesFence(line, open string) bool {
	trimmed := strings.TrimSpace(line)
	if open == "" || len(trimmed) < len(open) {
		return false
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != open[0] {
			return false
		}
	}
	return true
}

var questionEnding = regexp.MustCompile(`(까요|인가요|나요|니까|가요|을까|ㄹ까|까)[.!]*$`)

// asks reports a question put to the person: an interrogative mark or a Korean
// sentence-final question form.
func asks(lines []string) bool {
	for _, line := range lines {
		if strings.ContainsAny(line, "?？") {
			return true
		}
		if questionEnding.MatchString(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

var internalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b[a-z]{2,}-[0-9]{2,}\b`),      // issue and case keys
	regexp.MustCompile(`\b[0-9a-fA-F]{16,}\b`),             // digests and revision ids
	regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-`), // UUIDs
	regexp.MustCompile(`\b(agent|local|artifact|history|issue|pr|mcp|cfg)://`),
	regexp.MustCompile(`\.sock\b`),
	regexp.MustCompile(`\bsystem/(core|run|bin)\b`),
	regexp.MustCompile(`\b(conv|evt|turn|run|agent|reply)_[A-Za-z0-9]{6,}\b`),
	regexp.MustCompile(`/(home|data|tmp|srv)/`),
}

// internal reports a retired alias, internal code or state identifier in the
// person's view.
func internal(lines []string, aliases []string) bool {
	for _, line := range lines {
		for _, pattern := range internalPatterns {
			if pattern.MatchString(line) {
				return true
			}
		}
		lower := strings.ToLower(line)
		for _, alias := range aliases {
			if strings.Contains(lower, strings.ToLower(alias)) {
				return true
			}
		}
	}
	return false
}

var (
	koreanDoneMarkers = []string{
		"완료", "끝냈", "마쳤", "처리했", "보냈", "올렸", "병합", "배포", "반영했", "설치했", "고쳤",
	}
	// English markers are whole words. "present" contains "sent", and
	// "abandoned" contains "done", but neither is a completion claim.
	englishDone     = regexp.MustCompile(`\b(?:done|merged|deployed|shipped|sent|fixed|completed|pushed|installed)\b`)
	evidenceMarkers = []string{
		"확인", "검증", "읽어", "열어", "재현", "로그", "결과", "미확인", "추정", "가정",
		"not verified", "unverified", "verified", "checked", "reproduced", "read back",
	}
)

// doneEvidence requires a done statement to carry an evidence or not-verified
// clause in the same reply. The design forbids a bare completion claim.
func claimsDone(text string) bool {
	for _, marker := range koreanDoneMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return englishDone.MatchString(text)
}

func doneEvidence(lines []string) bool {
	text := strings.ToLower(strings.Join(lines, "\n"))
	if !claimsDone(text) {
		return true
	}
	for _, marker := range evidenceMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// language requires every visible prose line of ten or more letters to be in
// the person's language. Korean needs Hangul. English is Latin letters, so
// Hangul, Han, kana, Cyrillic and any other non-Latin script fail an English line.
func language(lines []string, want string) bool {
	for _, line := range lines {
		letters, hangul := 0, 0
		nonLatin := false
		for _, r := range line {
			if !unicode.IsLetter(r) {
				continue
			}
			letters++
			if unicode.In(r, unicode.Hangul) {
				hangul++
			}
			if !unicode.In(r, unicode.Latin) {
				nonLatin = true
			}
		}
		if letters < 10 {
			continue
		}
		switch want {
		case "ko":
			if hangul == 0 {
				return false
			}
		case "en":
			if nonLatin {
				return false
			}
		}
	}
	return true
}
