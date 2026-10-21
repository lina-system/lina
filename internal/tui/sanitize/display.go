// SPDX-License-Identifier: Apache-2.0

// Package sanitize removes terminal instructions from untrusted display text.
package sanitize

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Stream is a value-type filter. Retain it across deltas so a split escape
// sequence or UTF-8 character cannot escape filtering. Its zero value is ready.
type Stream struct {
	mode    uint8
	pending string
}

const (
	plain uint8 = iota
	escape
	intermediate
	csi
	controlString
	stringEscape
)

// Feed returns only complete, safe text and the next filter value. It does not
// modify the receiver. Newlines remain structural; tabs become four spaces.
func (s Stream) Feed(text string) (Stream, string) {
	text = s.pending + text
	s.pending = ""
	var out strings.Builder
	for len(text) > 0 {
		if !utf8.FullRuneInString(text) {
			s.pending = text
			break
		}
		r, n := utf8.DecodeRuneInString(text)
		text = text[n:]
		switch s.mode {
		case controlString:
			if r == '\a' || r == '\u009c' {
				s.mode = plain
			} else if r == '\x1b' {
				s.mode = stringEscape
			}
		case stringEscape:
			if r == '\\' || r == '\a' || r == '\u009c' {
				s.mode = plain
			} else if r != '\x1b' {
				s.mode = controlString
			}
		case csi:
			if r == '\x1b' {
				s.mode = escape
			} else if r >= 0x40 && r <= 0x7e {
				s.mode = plain
			}
		case escape:
			switch r {
			case '[':
				s.mode = csi
			case ']', 'P', 'X', '^', '_':
				s.mode = controlString
			case '\x1b':
			default:
				if r >= 0x20 && r <= 0x2f {
					s.mode = intermediate
				} else {
					s.mode = plain
				}
			}
		case intermediate:
			if r == '\x1b' {
				s.mode = escape
			} else if r < 0x20 || r > 0x2f {
				s.mode = plain
			}
		case plain:
			switch r {
			case '\x1b':
				s.mode = escape
			case '\u009b':
				s.mode = csi
			case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
				s.mode = controlString
			case '\n':
				out.WriteRune(r)
			case '\t':
				out.WriteString("    ")
			default:
				if !unicode.IsControl(r) && !bidi(r) {
					out.WriteRune(r)
				}
			}
		}
	}
	return s, out.String()
}

func bidi(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u206f')
}

// Display filters one complete string. Incomplete terminal instructions are
// discarded; invalid UTF-8 is replaced. Sanitizing a result again is a no-op.
func Display(text string) string {
	s, out := (Stream{}).Feed(text)
	if s.mode == plain && s.pending != "" {
		out += strings.Repeat("�", len(s.pending))
	}
	return out
}
