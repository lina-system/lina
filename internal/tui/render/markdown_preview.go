// SPDX-License-Identifier: Apache-2.0

package render

import "strings"

// markdownPreview consumes the open tail once. Delimiters whose interpretation
// is still pending are withheld, while their text remains readable without
// styling. The sealed Goldmark parse decides literal/malformed syntax finally.
type markdownPreview struct {
	bullet      string
	atStart     bool
	leading     string
	fence       byte
	fenceLen    int
	fenceHeader bool
	// fenceStart is an offset in emitted preview text, not Markdown source.
	emitted, fenceStart   int
	fenceLines            int
	indented              bool
	destinationEscape     bool
	ticks, codeTicks      int
	underscores           int
	escaped, underscore   bool
	brackets, destination int
	afterLabel, reference bool
	previous              byte
}

func (p *markdownPreview) feed(delta string) string {
	var out strings.Builder
	for i := range len(delta) {
		inCode := p.fence != 0 && !p.fenceHeader
		p.consume(delta[i], &out)
		if inCode && p.fence != 0 && delta[i] == '\n' {
			p.fenceLines++
		}
	}
	p.emitted += out.Len()
	return out.String()
}

func (p *markdownPreview) consume(ch byte, out *strings.Builder) {
	if p.fenceHeader {
		if ch == '\n' {
			p.fenceHeader = false
			p.atStart = true
		}
		return
	}
	if p.indented {
		out.WriteByte(ch)
		if ch == '\n' {
			p.indented = false
			p.atStart = true
		}
		return
	}
	if p.atStart {
		p.leading += string([]byte{ch})
		trimmed := strings.TrimLeft(p.leading, " ")
		indentation := len(p.leading) - len(trimmed)
		if trimmed == "" && indentation <= 3 {
			return
		}
		if p.fence != 0 {
			run := strings.TrimRight(trimmed, string(p.fence))
			if run == "" {
				return
			}
			candidate := strings.TrimSpace(trimmed)
			if len(candidate) >= p.fenceLen && strings.Trim(candidate, string(p.fence)) == "" && ch == ' ' {
				return
			}
			if ch == '\n' && len(candidate) >= p.fenceLen && strings.Trim(candidate, string(p.fence)) == "" {
				p.fence, p.fenceLen, p.leading = 0, 0, ""
				return
			}
			out.WriteString(p.leading)
			p.leading = ""
			p.atStart = ch == '\n'
			return
		}
		if indentation == 4 && trimmed == "" {
			p.leading, p.atStart, p.indented = "", false, true
			return
		}
		if indentation <= 3 && len(trimmed) > 0 {
			first := trimmed[0]
			if first == '`' || first == '~' {
				count := 0
				for count < len(trimmed) && trimmed[count] == first {
					count++
				}
				if count >= 3 {
					// Keep counting the opener across chunk boundaries.
					if count == len(trimmed) {
						return
					}
					p.fence, p.fenceLen = first, count
					p.fenceStart = p.emitted + out.Len()
					p.fenceLines = 0
					p.leading = ""
					p.fenceHeader = ch != '\n'
					p.atStart = ch == '\n'
					return
				}
				if count == len(trimmed) {
					return
				}
			}
			if strings.ContainsRune("-*+#", rune(first)) {
				count := 0
				for count < len(trimmed) && trimmed[count] == first {
					count++
				}
				if count == len(trimmed) {
					return
				}
				if trimmed[count] == ' ' && (count == 1 || first == '#') {
					if first != '#' {
						out.WriteString(strings.Repeat(" ", indentation) + p.bullet + " ")
					}
					p.leading, p.atStart = "", false
					return
				}
			}
			if first >= '0' && first <= '9' {
				count := 0
				for count < len(trimmed) && trimmed[count] >= '0' && trimmed[count] <= '9' {
					count++
				}
				if count <= 9 && (count == len(trimmed) || count+1 == len(trimmed) && (ch == '.' || ch == ')')) {
					return
				}
				if count+2 == len(trimmed) && (trimmed[count] == '.' || trimmed[count] == ')') && ch == ' ' {
					out.WriteString(strings.Repeat(" ", indentation) + trimmed[:count] + ". ")
					p.leading, p.atStart = "", false
					return
				}
			}
		}
		value := p.leading
		p.leading, p.atStart = "", false
		for i := range len(value) {
			p.inline(value[i], out)
		}
		return
	}
	if p.fence != 0 {
		out.WriteByte(ch)
		if ch == '\n' {
			p.atStart = true
		}
		return
	}
	p.inline(ch, out)
}

func (p *markdownPreview) inline(ch byte, out *strings.Builder) {
	if ch == '\n' {
		p.atStart = true
	}
	if p.destination > 0 {
		if p.destinationEscape {
			p.destinationEscape = false
			return
		}
		if ch == '\\' {
			p.destinationEscape = true
			return
		}
		if ch == '(' {
			p.destination++
		}
		if ch == ')' {
			p.destination--
		}
		return
	}
	if p.escaped {
		out.WriteByte(ch)
		p.escaped = false
		p.previous = ch
		return
	}
	if ch == '`' {
		p.ticks++
		return
	}
	if p.ticks > 0 {
		if p.codeTicks == 0 {
			p.codeTicks = p.ticks
		} else if p.codeTicks == p.ticks {
			p.codeTicks = 0
		} else {
			out.WriteString(strings.Repeat("`", p.ticks))
		}
		p.ticks = 0
	}
	if p.codeTicks != 0 {
		out.WriteByte(ch)
		return
	}
	if ch == '\\' {
		p.escaped = true
		return
	}
	if ch == '_' {
		p.underscores++
		return
	}
	if p.underscores > 0 {
		if p.underscore {
			p.underscore = false
		} else if p.previous == 0 || p.previous == ' ' || p.previous == '\n' {
			p.underscore = true
		} else {
			out.WriteString(strings.Repeat("_", p.underscores))
		}
		p.underscores = 0
	}
	if p.reference {
		if ch == ']' {
			p.reference = false
		}
		return
	}
	if p.afterLabel {
		p.afterLabel = false
		if ch == '(' {
			p.destination = 1
			return
		}
		if ch == '[' {
			p.reference = true
			return
		}
	}
	switch ch {
	case '*':
		return
	case '[':
		p.brackets++
		return
	case ']':
		if p.brackets > 0 {
			p.brackets--
			p.afterLabel = p.brackets == 0
			return
		}
	}
	out.WriteByte(ch)
	p.previous = ch
}
