// SPDX-License-Identifier: Apache-2.0

package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
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

// murmur's URL rules mask only the userinfo password and the values of
// sensitive query or fragment names. Hosts, paths, names, separators and other
// values stay, so plain URLs, ids and hashes pass through. Generic prose rules
// do not run inside a URL, where their value boundary stops at '&'.
var (
	urlPattern = regexp.MustCompile("(?i)https?://[^\\s<>\"'`]+")
	// A sensitive name contains a credential word, or one of its parts is a
	// credential word or ends with one: access_token, clientSecret,
	// X-Amz-Signature.
	sensitiveKey   = regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key|authorization|cookie|credential|private[_-]?key)`)
	sensitiveParts = map[string]bool{
		"key": true, "token": true, "secret": true, "sig": true, "signature": true,
		"auth": true, "authorization": true, "password": true, "passwd": true, "pwd": true,
		"credential": true, "credentials": true, "session": true, "apikey": true,
	}
	sensitiveSuffixes = []string{"token", "secret", "password", "passwd", "apikey", "auth"}
)

// urlRegions returns each URL's byte range without trailing sentence
// punctuation or an unmatched closing bracket.
func urlRegions(text string) [][2]int {
	var regions [][2]int
	for _, match := range urlPattern.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		for end > start {
			closer := text[end-1]
			switch closer {
			case '.', ',', ';', ':', '!', '?':
				end--
				continue
			case ')', ']', '}':
				if strings.Count(text[start:end], string(closer)) > strings.Count(text[start:end], string(map[byte]byte{')': '(', ']': '[', '}': '{'}[closer])) {
					end--
					continue
				}
			}
			break
		}
		if end > start {
			regions = append(regions, [2]int{start, end})
		}
	}
	return regions
}

// urlValueSpans addresses the password and sensitive pair values inside one URL.
// Offsets address its original bytes, never a decoded copy.
func urlValueSpans(text string, region [2]int) []Span {
	start, end := region[0], region[1]
	var spans []Span
	schemeEnd := start + strings.Index(text[start:end], "://") + 3
	authorityEnd := end
	if cut := strings.IndexAny(text[schemeEnd:end], "/?#"); cut >= 0 {
		authorityEnd = schemeEnd + cut
	}
	if at := strings.LastIndexByte(text[schemeEnd:authorityEnd], '@'); at >= 0 {
		at += schemeEnd
		if colon := strings.IndexByte(text[schemeEnd:at], ':'); colon >= 0 && schemeEnd+colon+1 < at {
			spans = append(spans, Span{Start: schemeEnd + colon + 1, End: at})
		}
	}
	for at := authorityEnd; at < end; at++ {
		if text[at] != '?' && text[at] != '#' {
			continue
		}
		segmentEnd := end
		if next := strings.IndexAny(text[at+1:end], "?#"); next >= 0 {
			segmentEnd = at + 1 + next
		}
		for position := at + 1; position < segmentEnd; {
			pieceEnd := segmentEnd
			if next := strings.IndexAny(text[position:segmentEnd], "&;"); next >= 0 {
				pieceEnd = position + next
			}
			piece := text[position:pieceEnd]
			if equal := strings.IndexByte(piece, '='); equal > 0 && equal+1 < len(piece) && sensitiveURLName(piece[:equal]) {
				spans = append(spans, Span{Start: position + equal + 1, End: pieceEnd})
			}
			position = pieceEnd + 1
		}
		at = segmentEnd - 1
	}
	return spans
}

// sensitiveURLName decides a pair name after decoding it, as murmur does.
func sensitiveURLName(raw string) bool {
	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		decoded = raw
	}
	decoded = strings.ToLower(decoded)
	if sensitiveKey.MatchString(decoded) {
		return true
	}
	for _, part := range strings.FieldsFunc(decoded, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || unicode.IsSpace(r)
	}) {
		if sensitiveParts[part] {
			return true
		}
		for _, suffix := range sensitiveSuffixes {
			if strings.HasSuffix(part, suffix) {
				return true
			}
		}
	}
	return false
}

func insideURL(regions [][2]int, at int) bool {
	for _, region := range regions {
		if at >= region[0] && at < region[1] {
			return true
		}
	}
	return false
}

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
	// Known credential shapes are masked everywhere, including inside URLs.
	// Named assignments and authorization values are prose rules and stop at a
	// URL, whose own pairs and password the URL rules decide.
	urls := urlRegions(text)
	for _, region := range urls {
		spans = append(spans, urlValueSpans(text, region)...)
	}
	for _, pattern := range []*regexp.Regexp{assignment, authorization} {
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			start, end := match[2], match[3]
			if start < 0 || insideURL(urls, start) {
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
