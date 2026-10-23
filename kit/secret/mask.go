// SPDX-License-Identifier: Apache-2.0

package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrSpan = errors.New("invalid_secret_span")

// Span uses half-open UTF-8 byte offsets. Mask accepts offsets into its input;
// Masked.Spans returns offsets into the replaced text, never the original.
type Span struct {
	Start       int    `json:"start"`
	End         int    `json:"end"`
	Sensitivity string `json:"sensitivity"`
}

// Masked contains only redacted text and flags. Construct it with Mask before
// native storage. DerivedText is the projection for indexes, summary inputs,
// ContextPackets and exports; Text retains placeholders for display/storage.
type Masked struct {
	text  string
	spans []Span
}

func (m Masked) Text() string  { return m.text }
func (m Masked) Spans() []Span { return append([]Span(nil), m.spans...) }

func (m Masked) DerivedText() string {
	var out strings.Builder
	start := 0
	for _, span := range m.spans {
		out.WriteString(m.text[start:span.Start])
		// Do not manufacture a new word by joining the edges of a removed span.
		if span.Start > 0 && span.End < len(m.text) && !space(m.text[span.Start-1]) && !space(m.text[span.End]) {
			out.WriteByte(' ')
		}
		start = span.End
	}
	out.WriteString(m.text[start:])
	return out.String()
}

func space(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

var shapes = []*regexp.Regexp{
	regexp.MustCompile(`(?:sk-(?:proj-|ant-api[0-9]+-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AIza[A-Za-z0-9_-]{35}|xox[baprs]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[A-Z0-9]{16})`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
}

// Recognize our exact placeholder only at a value boundary. That alternative
// consumes the boundary without capturing it, preserving punctuation on recheck
// while leaving a glued credential suffix to the captured value alternative.
const assignmentBoundary = `(?:$|[\s,;"'<>}\]).])`

var assignment = regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd|secret|token)["']?\s*[:=]\s*(?:(?-i:` + regexp.QuoteMeta(Placeholder) + `)` + assignmentBoundary + `|("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\s,;"'<>]+))`)
var afterAssignment = regexp.MustCompile(`^` + assignmentBoundary)
var authorization = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+([A-Za-z0-9._~+/=-]+)`)
var privateKey = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----`)

// Mask discards detected secret-shaped values and explicitly flagged spans.
// Detectors are conservative heuristics, not a guarantee for arbitrary unknown
// credentials. Callers with sensitivity metadata must pass those spans too.
func Mask(text string, flagged ...Span) (Masked, error) {
	if !utf8.ValidString(text) {
		return Masked{}, ErrSpan
	}
	trimmed := strings.TrimSpace(text)
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(text)) {
		return maskJSON(text, flagged)
	}
	spans := append([]Span(nil), flagged...)
	for _, span := range spans {
		if !validSpan(text, span) || span.Sensitivity != "" && span.Sensitivity != "secret" {
			return Masked{}, ErrSpan
		}
	}
	for _, pattern := range shapes {
		for _, match := range pattern.FindAllStringIndex(text, -1) {
			spans = append(spans, Span{Start: match[0], End: match[1]})
		}
	}
	for _, pattern := range []*regexp.Regexp{assignment, authorization} {
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			start, end := match[2], match[3]
			if start < 0 {
				continue
			}
			if pattern == assignment && (text[start] == '"' || text[start] == '\'') && !afterAssignment.MatchString(text[end:]) {
				// Keep the quotes when removing them would glue public text to
				// the placeholder and make a later check mask that text too.
				start++
				end--
			}
			if text[start:end] != Placeholder {
				spans = append(spans, Span{Start: start, End: end})
			}
		}
	}
	for _, match := range privateKey.FindAllStringIndex(text, -1) {
		end := len(text)
		marker := strings.Replace(text[match[0]:match[1]], "BEGIN", "END", 1)
		if offset := strings.Index(text[match[1]:], marker); offset >= 0 {
			end = match[1] + offset + len(marker)
		}
		spans = append(spans, Span{Start: match[0], End: end})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	merged := make([]Span, 0, len(spans))
	for _, span := range spans {
		if n := len(merged); n > 0 && span.Start <= merged[n-1].End {
			merged[n-1].End = max(merged[n-1].End, span.End)
		} else {
			merged = append(merged, span)
		}
	}
	var out strings.Builder
	var result Masked
	start := 0
	for _, span := range merged {
		out.WriteString(text[start:span.Start])
		masked := Span{Start: out.Len(), End: out.Len() + len(Placeholder), Sensitivity: "secret"}
		out.WriteString(Placeholder)
		result.spans = append(result.spans, masked)
		start = span.End
	}
	out.WriteString(text[start:])
	result.text = out.String()
	return result, nil
}

func validSpan(text string, span Span) bool {
	return span.Start >= 0 && span.Start < span.End && span.End <= len(text) &&
		utf8.RuneStart(text[span.Start]) && (span.End == len(text) || utf8.RuneStart(text[span.End]))
}

type record struct {
	Text  string `json:"text"`
	Spans []Span `json:"spans"`
}

func (m Masked) MarshalJSON() ([]byte, error) { return json.Marshal(record{m.text, m.spans}) }

// UnmarshalJSON only accepts already-masked records, never an alternative way
// to introduce raw text. Invalid input leaves the receiver unchanged.
func (m *Masked) UnmarshalJSON(data []byte) error {
	var r record
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return ErrSpan
	}
	end := 0
	for _, span := range r.Spans {
		if !validSpan(r.Text, span) || span.Start < end || span.Sensitivity != "secret" || r.Text[span.Start:span.End] != Placeholder {
			return ErrSpan
		}
		end = span.End
	}
	checked, err := Mask(r.Text)
	if err != nil || checked.text != r.Text {
		return ErrSpan
	}
	*m = Masked{text: r.Text, spans: r.Spans}
	return nil
}
