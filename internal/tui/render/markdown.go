// SPDX-License-Identifier: Apache-2.0

package render

import (
	"sort"
	"strconv"
	"strings"
)

// InlineStyle is semantic data. Only the UI adapter produces terminal escapes.
type InlineStyle struct {
	Bold, Italic, Code, Link, Muted bool
	URL                             string
}
type Span struct {
	Text  string
	Style InlineStyle
}
type StyledRow struct {
	Spans   []Span
	Clipped bool
}

// MarkdownBlock is a parsed block with a hanging list prefix, before layout.
type MarkdownBlock struct {
	Spans  []Span
	Prefix string
	Indent int
}

// MarkdownParser belongs to the UI adapter; render owns no parser dependency.
type MarkdownParser func(source, bullet string) []MarkdownBlock
type markdownCache struct {
	end, read  int
	sealed     bool
	blocks     []MarkdownBlock
	preview    markdownPreview
	tail       *Stream
	config     Config
	bullet     string
	rows       []StyledRow
	closedRows int
	tailRows   []StyledRow
}

// Markdown returns semantic rows, wrapping visible text before applying styles.
// Closed source is parsed once; the open tail consumes only new bytes and hides
// pending delimiters. Repeated views reuse the rows until width or input changes.
func (s *Stream) Markdown(config Config, bullet string, parse MarkdownParser) []StyledRow {
	if config.Width <= 0 {
		return nil
	}
	if s.markdown == nil {
		s.markdown = &markdownCache{}
	}
	c := s.markdown
	if c.bullet != "" && c.bullet != bullet {
		s.markdown = &markdownCache{}
		c = s.markdown
	}
	source := s.text.String()
	if c.sealed == s.sealed && c.read == len(source) && c.config == config && c.bullet == bullet && c.rows != nil {
		return c.rows
	}
	end := s.frozen
	if s.sealed {
		end = len(source)
	}
	oldBlocks := len(c.blocks)
	rebuild := c.config != config || c.bullet != bullet || c.rows == nil
	if end > c.end || s.sealed && !c.sealed {
		// Seal is authoritative, including reference definitions supplied at the end.
		if s.sealed {
			c.blocks = parse(source, bullet)
			rebuild = true
		} else {
			c.blocks = append(c.blocks, parse(source[c.end:end], bullet)...)
			if strings.HasSuffix(source[:end], "\n\n") {
				c.blocks = append(c.blocks, MarkdownBlock{})
			}
		}
		c.end, c.read = end, end
		c.preview = markdownPreview{bullet: bullet, atStart: true}
		c.tail = nil
		c.tailRows = nil
	}
	if c.tail == nil {
		c.tail = NewStream("preview", config, nil)
		c.preview.bullet = bullet
		c.preview.atStart = true
	}
	if c.tail.config != config {
		previous := c.tail.text.String()
		c.tail = NewStream("preview", config, nil)
		c.tail.text.WriteString(previous)
		c.tail.lines = wrap(previous, config.Width, c.tail.measurer, 0, s.revision)
		c.tailRows = nil
	}
	if c.read < len(source) {
		c.tail.Append(s.revision, c.preview.feed(source[c.read:]))
	}
	if rebuild {
		c.rows = nil
		oldBlocks = 0
	} else {
		c.rows = c.rows[:c.closedRows]
	}
	for _, block := range c.blocks[oldBlocks:] {
		c.rows = append(c.rows, wrapMarkdown(block, config)...)
	}
	c.closedRows = len(c.rows)
	if !s.sealed && len(source) > c.end {
		// Stream.Append revisits just its boundary row and last partial row.
		start := max(0, len(c.tailRows)-2)
		c.tailRows = c.tailRows[:start]
		for i := start; i < c.tail.Len(); i++ {
			row := c.tail.At(i)
			c.tailRows = append(c.tailRows, StyledRow{Spans: []Span{{Text: row.Text}}, Clipped: row.Clipped})
		}
		c.rows = append(c.rows, c.codePreview(config)...)
	}
	c.read, c.sealed, c.config, c.bullet = len(source), s.sealed, config, bullet
	return c.rows
}

// codePreview bounds only the open fence. Keep the complete incremental row
// index so closing the fence or sealing the item can reveal the full source.
func (c *markdownCache) codePreview(config Config) []StyledRow {
	p := &c.preview
	if p.fence == 0 || p.fenceHeader {
		return c.tailRows
	}
	start := sort.Search(len(c.tail.lines), func(i int) bool {
		return c.tail.lines[i].start >= p.fenceStart
	})
	end := len(c.tailRows)
	// A newline creates an empty cursor row; it is not another code line.
	if end > start && c.tail.lines[end-1].start == c.tail.text.Len() {
		end--
	}
	if end-start <= 8 {
		return c.tailRows
	}
	lines := p.fenceLines
	if !p.atStart || p.leading != "" {
		lines++
	}
	rows := make([]StyledRow, 0, start+7)
	rows = append(rows, c.tailRows[:start]...)
	rows = append(rows, codeNotice(lines, config))
	return append(rows, c.tailRows[end-6:end]...)
}

func codeNotice(lines int, config Config) StyledRow {
	label := "writing code… (" + strconv.Itoa(lines) + " lines)"
	label = Truncate(label, config.Width, Unicode{WidthMode: config.Mode})
	return StyledRow{Spans: []Span{{Text: label, Style: InlineStyle{Muted: true}}}}
}

// CodePreview applies the same open-fence bound to plain result Details.
// Outside a collapsed fence it preserves the existing literal-text view.
func (s *Stream) CodePreview(config Config) []StyledRow {
	if config.Width <= 0 {
		return nil
	}
	lines := s.lines
	if config.Width != s.config.Width || config.Mode != s.config.Mode {
		lines = wrap(s.text.String(), config.Width, Unicode{WidthMode: config.Mode}, 0, s.revision)
	}
	start, end, count := len(lines), len(lines), 0
	if !s.sealed && s.fence != 0 {
		body := sort.Search(len(lines), func(i int) bool { return lines[i].start >= s.fenceBody })
		if end > body && lines[end-1].start == s.text.Len() {
			end--
		}
		if end-body > 8 {
			start = sort.Search(len(lines), func(i int) bool { return lines[i].start >= s.fenceStart })
			count = s.fenceLines
			if s.lineStart < s.text.Len() {
				count++
			}
		}
	}
	var rows []StyledRow
	for i := 0; i < len(lines); i++ {
		if i == start {
			rows = append(rows, codeNotice(count, config))
			i = end - 6
		}
		if start < len(lines) && i >= end {
			break
		}
		line := lines[i]
		rows = append(rows, StyledRow{Spans: []Span{{Text: s.text.String()[line.start:line.end]}}, Clipped: line.clipped})
	}
	return rows
}

func wrapMarkdown(block MarkdownBlock, config Config) []StyledRow {
	m := Unicode{WidthMode: config.Mode}
	prefix := strings.Repeat(" ", block.Indent) + block.Prefix
	indent := m.Width(prefix)
	// Keep at least one content cell at narrow widths, clipping decoration first.
	if indent >= config.Width {
		prefix = Truncate(prefix, max(0, config.Width-1), m)
		indent = m.Width(prefix)
	}
	var text strings.Builder
	ends := make([]int, len(block.Spans))
	for i, span := range block.Spans {
		text.WriteString(span.Text)
		ends[i] = text.Len()
	}
	lines := wrap(text.String(), max(1, config.Width-indent), m, 0, 0)
	rows := make([]StyledRow, 0, len(lines))
	spanIndex := 0
	for i, line := range lines {
		row := StyledRow{Clipped: line.clipped}
		if prefix != "" {
			label := prefix
			if i > 0 {
				label = strings.Repeat(" ", indent)
			}
			row.Spans = append(row.Spans, Span{Text: label})
		}
		for spanIndex < len(ends) && ends[spanIndex] <= line.start {
			spanIndex++
		}
		start := line.start
		for j := spanIndex; j < len(ends) && start < line.end; j++ {
			end := min(line.end, ends[j])
			row.Spans = append(row.Spans, Span{Text: text.String()[start:end], Style: block.Spans[j].Style})
			start = end
		}
		rows = append(rows, row)
	}
	return rows
}
