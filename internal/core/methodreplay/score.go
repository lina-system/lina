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
// count against the reply.
func proseLines(text string) []string {
	var out []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		out = append(out, line)
	}
	return out
}

var questionEnding = regexp.MustCompile(`(까|나요|니까|가요|을까|ㄹ까|인가요)[.!]*$`)

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
	doneMarkers = []string{
		"완료", "끝냈", "마쳤", "처리했", "보냈", "올렸", "병합", "배포", "반영했", "설치했", "고쳤",
		"done", "merged", "deployed", "shipped", "sent", "fixed", "completed", "pushed", "installed",
	}
	evidenceMarkers = []string{
		"확인", "검증", "읽어", "열어", "재현", "로그", "결과", "미확인", "추정", "가정",
		"not verified", "unverified", "verified", "checked", "reproduced", "read back",
	}
)

// doneEvidence requires a done statement to carry an evidence or not-verified
// clause in the same reply. The design forbids a bare completion claim.
func doneEvidence(lines []string) bool {
	text := strings.ToLower(strings.Join(lines, "\n"))
	done := false
	for _, marker := range doneMarkers {
		if strings.Contains(text, marker) {
			done = true
			break
		}
	}
	if !done {
		return true
	}
	for _, marker := range evidenceMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// language requires every visible prose line to be in the person's language.
func language(lines []string, want string) bool {
	for _, line := range lines {
		letters, hangul := 0, 0
		for _, r := range line {
			if !unicode.IsLetter(r) {
				continue
			}
			letters++
			if unicode.In(r, unicode.Hangul) {
				hangul++
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
			if hangul > 0 {
				return false
			}
		}
	}
	return true
}
