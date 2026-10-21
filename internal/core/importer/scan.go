// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"regexp"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/kit/secret"
)

// Span uses half-open byte offsets into the ORIGINAL body or sidecar. Imported
// evidence is never replaced on disk by a mask; consumers must use Project.
type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Kind  string `json:"kind"`
}

var secretShapes = []*regexp.Regexp{
	regexp.MustCompile(`(?:sk-(?:proj-|ant-api[0-9]+-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|AIza[A-Za-z0-9_-]{35}|xox[baprs]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[A-Z0-9]{16})`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
}
var assignments = regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd|secret|token)["']?\s*[:=]\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|[^\s,;"'<>\x00]+)`)
var auth = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+([A-Za-z0-9._~+/=-]+)`)
var keyBlock = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----`)

// ScanSecrets is deterministic and byte-preserving, including for binary
// sidecars. Heuristics flag recognized shapes, not arbitrary unknown secrets.
// An unterminated private key flags everything from its header to end of input.
func ScanSecrets(body []byte) []Span {
	var found []Span
	add := func(start, end int) { found = append(found, Span{Start: start, End: end, Kind: "secret"}) }
	for _, p := range secretShapes {
		for _, m := range p.FindAllIndex(body, -1) {
			add(m[0], m[1])
		}
	}
	for _, p := range []*regexp.Regexp{assignments, auth} {
		for _, m := range p.FindAllSubmatchIndex(body, -1) {
			add(m[2], m[3])
		}
	}
	for _, m := range keyBlock.FindAllIndex(body, -1) {
		end := len(body)
		marker := strings.Replace(string(body[m[0]:m[1]]), "BEGIN", "END", 1)
		if i := bytes.Index(body[m[1]:], []byte(marker)); i >= 0 {
			end = m[1] + i + len(marker)
		}
		add(m[0], end)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Start == found[j].Start {
			return found[i].End < found[j].End
		}
		return found[i].Start < found[j].Start
	})
	var merged []Span
	for _, span := range found {
		if n := len(merged); n > 0 && span.Start <= merged[n-1].End {
			merged[n-1].End = max(merged[n-1].End, span.End)
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}

// Project masks spans for agent display or excludes them from derived inputs
// (indexes, summaries, requests and exports). A separating space prevents the
// removal from manufacturing a new word. Invalid flags fail closed.
func Project(body []byte, spans []Span, derived bool) ([]byte, error) {
	var out bytes.Buffer
	start := 0
	for _, span := range spans {
		if span.Kind != "secret" || span.Start < start || span.End <= span.Start || span.End > len(body) {
			return nil, ErrSnapshot
		}
		out.Write(body[start:span.Start])
		if derived {
			out.WriteByte(' ')
		} else {
			out.WriteString(secret.Placeholder)
		}
		start = span.End
	}
	out.Write(body[start:])
	return out.Bytes(), nil
}
