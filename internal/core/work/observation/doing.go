// SPDX-License-Identifier: Apache-2.0
// Package observation holds safe worker display evidence shared by the ledger
// and adapters. It has no dependency on worker protocols or user interfaces.
package observation

import (
	"github.com/thisisjun786/lina/internal/core/secrets"
	"strings"
	"time"
	"unicode"
)

// Snapshot is display-only evidence, never a work verdict. Since is the last
// observed busy/idle transition; a zero instant means it has not been observed.
type Snapshot struct {
	Line  string    `json:"line"`
	Busy  bool      `json:"busy"`
	Since time.Time `json:"since,omitzero"`
}

// Clean masks complete values before shortening, removes terminal escapes and
// bidi controls, and makes markup inert on both terminal and channel surfaces.
func Clean(s string) string {
	if len(s) > 2<<20 {
		return ""
	}
	masked, err := secrets.Mask(s)
	if err != nil {
		return ""
	}
	s = stripControls(masked.Text())
	s = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return ' '
		}
		if unicode.In(c, unicode.Cf) {
			return -1
		}
		if strings.ContainsRune("<>[]`*_", c) {
			return -1
		}
		return c
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	chars := []rune(s)
	if len(chars) > 120 {
		s = string(chars[:119]) + "…"
	}
	return s
}

// stripControls consumes complete terminal strings and CSI/escape sequences,
// including their C1 forms. An unfinished sequence has no display content.
func stripControls(s string) string {
	const (
		plain = iota
		escape
		csi
		control
		controlEscape
		intermediate
	)
	mode := plain
	var out strings.Builder
	for _, r := range s {
		switch mode {
		case control:
			if r == '\a' || r == '\u009c' {
				mode = plain
			} else if r == '\x1b' {
				mode = controlEscape
			}
		case controlEscape:
			if r == '\\' || r == '\a' || r == '\u009c' {
				mode = plain
			} else if r != '\x1b' {
				mode = control
			}
		case csi:
			if r == '\x1b' {
				mode = escape
			} else if r >= 0x40 && r <= 0x7e {
				mode = plain
			}
		case escape:
			switch r {
			case '[':
				mode = csi
			case ']', 'P', 'X', '^', '_':
				mode = control
			case '\x1b':
			default:
				if r >= 0x20 && r <= 0x2f {
					mode = intermediate
				} else {
					mode = plain
				}
			}
		case intermediate:
			if r == '\x1b' {
				mode = escape
			} else if r < 0x20 || r > 0x2f {
				mode = plain
			}
		default:
			switch r {
			case '\x1b':
				mode = escape
			case '\u009b':
				mode = csi
			case '\u0090', '\u0098', '\u009d', '\u009e', '\u009f':
				mode = control
			default:
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}
