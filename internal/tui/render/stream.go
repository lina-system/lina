// SPDX-License-Identifier: Apache-2.0

// Package render builds incremental, unstyled rows and viewport render patches.
// Terminal styles and I/O belong to the UI adapter, not this package.
package render

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/tui/sanitize"
)

type Config struct {
	Width int
	Theme string
	Mode  WidthMode
}
type Row struct {
	ID            string
	Revision      uint64
	Text          string
	Offset        int
	Done, Clipped bool
}
type Source interface {
	Len() int
	At(int) Row
}

// Rows adapts a reflowed window to the viewport renderer.
type Rows []Row

func (r Rows) Len() int     { return len(r) }
func (r Rows) At(i int) Row { return r[i] }

type line struct {
	start, end int
	revision   uint64
	clipped    bool
}

// Stream indexes one ledger item's plain-text rows. Append only rewraps the last
// partial row and its boundary; complete rows keep their identity and revision. Text is stored
// once in an append-only builder, avoiding quadratic copying on long streams.
// A Stream must not be copied after its first Append or Seal.
type closure struct {
	end      int
	revision uint64
}

type Stream struct {
	closures                   []closure
	id                         string
	config                     Config
	measurer                   Measurer
	text                       strings.Builder
	filter                     sanitize.Stream
	lines                      []line
	revision                   uint64
	sealed                     bool
	frozen, scanned, lineStart int
	fence                      byte
	fenceLen                   int
}

func NewStream(id string, config Config, m Measurer) *Stream {
	if m == nil {
		m = Unicode{WidthMode: config.Mode}
	}
	config.Width = max(1, config.Width)
	return &Stream{id: id, config: config, measurer: m, lines: []line{{}}}
}
func (s *Stream) Len() int { return len(s.lines) }
func (s *Stream) At(i int) Row {
	l := s.lines[i]
	return Row{ID: s.id + ":" + strconv.Itoa(l.start), Revision: l.revision, Text: s.text.String()[l.start:l.end], Done: s.sealed || l.start < s.frozen && l.end <= s.frozen, Clipped: l.clipped, Offset: l.start}
}

// Append ignores replayed revisions and late deltas after Seal.
func (s *Stream) Append(revision uint64, delta string) {
	if s.sealed || revision <= s.revision {
		return
	}
	s.revision = revision
	var safe string
	s.filter, safe = s.filter.Feed(delta)
	if safe == "" {
		return
	}
	// Revisit the boundary row too: a late combining rune or ZWJ may join
	// the previous final cluster, including a clipped cluster or wrap delimiter.
	tail := max(0, len(s.lines)-2)
	start := 0
	if len(s.lines) > 0 {
		start = s.lines[tail].start
	}
	old := s.lines[tail:]
	s.text.WriteString(safe)
	s.freeze()
	updated := wrap(s.text.String(), s.config.Width, s.measurer, start, revision)
	for i := range updated {
		if i < len(old) && updated[i].start == old[i].start && updated[i].end == old[i].end && updated[i].clipped == old[i].clipped {
			updated[i].revision = old[i].revision
		}
	}
	s.lines = append(s.lines[:tail], updated...)
}

// Seal replaces streamed text with the authoritative item once. A newer stored
// revision may replace an already sealed item, but a replay cannot repaint it.
func (s *Stream) Seal(revision uint64, text string) {
	if revision < s.revision || (s.sealed && revision == s.revision) {
		return
	}
	s.revision = revision
	s.sealed = true
	s.filter = sanitize.Stream{}
	s.closures = nil
	s.text.Reset()
	s.text.WriteString(sanitize.Display(text))
	s.lines = wrap(s.text.String(), s.config.Width, s.measurer, 0, revision)
}

// Window reflows only the requested rows at a byte anchor from Row.Offset.
// Use it for resize instead of scanning the history to rediscover a row number.
// The caller selects a block by ledger id, keeps its anchor and adds overscan to
// rows. Wrapping and measuring stop as soon as this window is filled.
func (s *Stream) Window(anchor, rows int, config Config, m Measurer) []Row {
	if rows <= 0 || config.Width <= 0 {
		return nil
	}
	if m == nil {
		m = Unicode{WidthMode: config.Mode}
	}
	anchor = max(0, min(s.text.Len(), anchor))
	text := s.text.String()
	lines := wrapLimit(text, config.Width, m, anchor, s.revision, rows)
	result := make([]Row, len(lines))
	for i, l := range lines {
		if !s.sealed {
			at := sort.Search(len(s.closures), func(i int) bool { return s.closures[i].end > l.start })
			if at < len(s.closures) && l.end <= s.closures[at].end {
				l.revision = s.closures[at].revision
			}
		}
		result[i] = Row{ID: s.id + ":" + strconv.Itoa(l.start), Revision: l.revision, Offset: l.start, Text: text[l.start:l.end], Done: s.sealed || l.start < s.frozen && l.end <= s.frozen, Clipped: l.clipped}
	}
	return result
}

// freeze recognizes conservative top-level Markdown boundaries. Blank lines
// close paragraphs/lists/tables, headings close at newline, and a fenced block
// remains plain until its matching fence. Unclosed content freezes at Seal.
func (s *Stream) freeze() {
	text := s.text.String()
	for i := s.scanned; i < len(text); i++ {
		if text[i] != '\n' {
			continue
		}
		line := text[s.lineStart:i]
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		marker, count := byte(0), 0
		if indent <= 3 && len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			marker = trimmed[0]
			for count < len(trimmed) && trimmed[count] == marker {
				count++
			}
			if count < 3 {
				marker = 0
			}
		}
		if s.fence != 0 {
			if marker == s.fence && count >= s.fenceLen && strings.TrimSpace(trimmed[count:]) == "" {
				s.fence = 0
				s.frozen = i + 1
			}
		} else if marker != 0 {
			s.fence = marker
			s.fenceLen = count
		} else {
			heading := 0
			for heading < len(trimmed) && trimmed[heading] == '#' {
				heading++
			}
			if strings.TrimSpace(line) == "" || indent <= 3 && heading > 0 && heading <= 6 && (heading == len(trimmed) || trimmed[heading] == ' ' || trimmed[heading] == '\t') {
				s.frozen = i + 1
			}
		}
		s.lineStart = i + 1
	}
	s.scanned = len(text)
	if s.frozen > 0 && (len(s.closures) == 0 || s.closures[len(s.closures)-1].end < s.frozen) {
		s.closures = append(s.closures, closure{s.frozen, s.revision})
	}
}

// wrap preserves source offsets. A whitespace break excludes its delimiter
// from the painted row. Oversized clusters occupy a clipped empty row, never a
// partial cluster. Each finalized row is visited only while its input arrives.
func wrap(text string, width int, m Measurer, start int, revision uint64) []line {
	return wrapLimit(text, width, m, start, revision, 0)
}

func wrapLimit(text string, width int, m Measurer, start int, revision uint64, limit int) []line {
	var rows []line
	pos, cells := start, 0
	breakStart, breakEnd := -1, -1
	emit := func(end, next int, clipped bool) {
		rows = append(rows, line{start, end, revision, clipped})
		start = next
		pos = next
		cells = 0
		breakStart = -1
		breakEnd = -1
	}
	for pos < len(text) && (limit <= 0 || len(rows) < limit) {
		cluster, _ := nextCluster(text[pos:])
		end := pos + len(cluster)
		if cluster == "\n" {
			emit(pos, end, false)
			continue
		}
		n := m.Width(cluster)
		r, _ := utf8.DecodeRuneInString(cluster)
		space := unicode.IsSpace(r)
		if cells+n > width {
			if space && pos > start {
				emit(pos, end, false)
				continue
			}
			if breakStart > start {
				emit(breakStart, breakEnd, false)
				continue
			}
			if pos > start {
				emit(pos, pos, false)
				continue
			}
			emit(pos, end, true)
			continue
		}
		if space {
			breakStart = pos
			breakEnd = end
		}
		cells += n
		pos = end
	}
	if limit <= 0 || len(rows) < limit {
		rows = append(rows, line{start, len(text), revision, false})
	}
	return rows
}
