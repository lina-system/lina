// SPDX-License-Identifier: Apache-2.0

package render

import (
	"strings"

	"github.com/thisisjun786/lina/internal/tui/sanitize"
)

type WidthMode uint8

const (
	WCWidth WidthMode = iota
	Graphemes
)

// Measurer is shared by wrapping, clipping and cursor placement. Callers supply
// the mode used in Config; it is part of every render cache key.
type Measurer interface{ Width(string) int }
type Unicode struct{ WidthMode WidthMode }

// Width defaults to per-codepoint terminal widths. Grapheme mode is selected
// only after the terminal confirms mode 2027. Ambiguous width remains narrow.
func (m Unicode) Width(text string) int {
	cells := 0
	if m.WidthMode == Graphemes {
		for text != "" {
			cluster, rest := nextCluster(text)
			width := 0
			emoji := false
			for _, r := range cluster {
				width = max(width, runeWidth(r))
				emoji = emoji || property(r, emojiRanges) != 0
			}
			if emoji && strings.ContainsRune(cluster, '\ufe0f') {
				width = max(2, width)
			}
			if emoji && strings.ContainsRune(cluster, '\ufe0e') {
				width = 1
			}
			cells += width
			text = rest
		}
		return cells
	}
	for _, r := range text {
		cells += runeWidth(r)
	}
	return cells
}

func runeWidth(r rune) int {
	c := graphemeClass(r)
	if control(c) || c == gbExtend || c == gbZWJ || r >= 0x1160 && r <= 0x11ff || r >= 0xd7b0 && r <= 0xd7c6 || r >= 0xd7cb && r <= 0xd7fb {
		return 0
	}
	if r == 0x2e3a {
		return 3
	}
	if r == 0x2e3b {
		return 4
	}
	if c == gbRegionalIndicator || property(r, wideRanges) != 0 || property(r, emojiPresentationRanges) != 0 {
		return 2
	}
	return 1
}

// Truncate sanitizes before measuring and never splits a grapheme cluster.
// Newlines end a row; terminal styling is applied by the caller afterwards.
func Truncate(text string, width int, m Measurer) string {
	text = sanitize.Display(text)
	if width <= 0 {
		return ""
	}
	end, cells := 0, 0
	rest := text
	for rest != "" {
		cluster, next := nextCluster(rest)
		if cluster == "\n" || cluster == "\r\n" {
			break
		}
		n := m.Width(cluster)
		if cells+n > width {
			break
		}
		cells += n
		end += len(cluster)
		rest = next
	}
	return text[:end]
}

type CursorPosition struct{ Row, Column int }

// Cursor returns the cell position at a UTF-8 byte boundary in sanitized text.
// An offset inside a cluster is clamped to its start, including IME composition.
func Cursor(text string, offset, width int, m Measurer) CursorPosition {
	if width <= 0 {
		return CursorPosition{}
	}
	offset = max(0, min(len(text), offset))
	boundary := 0
	for rest := text; rest != ""; {
		cluster, next := nextCluster(rest)
		if boundary+len(cluster) > offset {
			break
		}
		boundary += len(cluster)
		rest = next
	}
	lines := wrap(text, width, m, 0, 0)
	pos := CursorPosition{}
	for i, line := range lines {
		if line.start > boundary {
			break
		}
		pos.Row = i
		pos.Column = m.Width(text[line.start:min(line.end, boundary)])
	}
	if pos.Column >= width {
		pos.Row++
		pos.Column = 0
	}
	return pos
}
