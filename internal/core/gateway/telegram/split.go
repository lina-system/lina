// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type codeFence struct {
	start, body, end, after int
	open, close             string
}

// Record whole fence lines before splitting so a chunk boundary cannot turn a
// language tag or closing delimiter into code. Preserve indentation and body
// whitespace; unlike the reference splitter, never lstrip the next chunk.
func codeFences(text string) []codeFence {
	var fences []codeFence
	var active *codeFence
	for start := 0; start < len(text); {
		end := start + strings.IndexByte(text[start:], '\n')
		if end < start {
			end = len(text)
		}
		after := min(end+1, len(text))
		line := strings.TrimSpace(text[start:end])
		if len(line) >= 3 && (line[0] == '`' || line[0] == '~') {
			n := 0
			for n < len(line) && line[n] == line[0] {
				n++
			}
			if n >= 3 {
				marker, info := line[:n], line[n:]
				if active == nil && (line[0] != '`' || !strings.Contains(info, "`")) {
					active = &codeFence{start: start, body: after, end: len(text), after: len(text), open: text[start:after], close: marker}
				} else if active != nil && marker[0] == active.close[0] && len(marker) >= len(active.close) && strings.TrimSpace(info) == "" {
					active.end, active.after = start, after
					fences = append(fences, *active)
					active = nil
				}
			}
		}
		start = after
	}
	if active != nil {
		fences = append(fences, *active)
	}
	return fences
}

func unitPrefix(s string, budget int) int {
	end := 0
	for i, r := range s {
		budget -= utf16.RuneLen(r)
		if budget < 0 {
			break
		}
		end = i + utf8.RuneLen(r)
	}
	return end
}

// The fence close/reopen structure follows Hermes's platform splitter. Shape
// still prepares at most three durable parts; Send never loops over chunks.
func splitText(text string) []string {
	if textFits(text, 4096) {
		return []string{text}
	}
	fences := codeFences(text)
	reserve := 0
	for _, f := range fences {
		reserve = max(reserve, units(f.close)+1)
	}
	var parts []string
	for start := 0; start < len(text) && len(parts) < 3; {
		prefix := ""
		for _, f := range fences {
			if start >= f.body && start <= f.end && start > f.start {
				prefix = f.open
				break
			}
		}
		if textFits(prefix+text[start:], 4096) {
			return append(parts, prefix+text[start:])
		}
		budget := 4096 - units(prefix) - reserve
		end := start + unitPrefix(text[start:], budget)
		if end <= start {
			return nil
		}
		region := text[start:end]
		// Prefer paragraphs; otherwise keep useful-sized lines/words intact.
		if i := strings.LastIndex(region, "\n\n"); i >= len(region)/2 {
			end = start + i + 2
		} else if i := strings.LastIndexByte(region, '\n'); i >= len(region)/2 {
			end = start + i + 1
		} else if i := strings.LastIndexByte(region, ' '); i >= len(region)/2 {
			end = start + i + 1
		}
		for _, f := range fences {
			if end > f.start && end <= f.body {
				end = f.start
			}
			if end > f.end && end < f.after {
				end = f.end
			}
		}
		if end <= start {
			return nil
		}
		part := prefix + text[start:end]
		for _, f := range fences {
			if end > f.body && end <= f.end {
				if !strings.HasSuffix(part, "\n") {
					part += "\n"
				}
				part += f.close
				break
			}
		}
		parts = append(parts, part)
		start = end
		if start == len(text) {
			return parts
		}
	}
	return nil
}
