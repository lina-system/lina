// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/render"
)

func (s Styles) listBullet() string {
	if s.options.ASCII {
		return s.tokens.GlyphAsciiAction
	}
	return s.tokens.GlyphUnicodeAction
}

// VisibleMarkdown is the sealed conversation text for source. bullet is the
// surface list marker. Link destinations are omitted. There are no terminal
// styles and no wrapping; callers compare this with the painted frame.
func VisibleMarkdown(source, bullet string) string {
	blocks := parseMarkdown(source, bullet)
	var out strings.Builder
	for i, block := range blocks {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(block.Prefix)
		if block.Table != nil {
			for i, row := range block.Table.Rows(int(^uint(0)>>1), render.Unicode{}) {
				if i > 0 {
					out.WriteByte('\n')
				}
				for _, span := range row.Spans {
					out.WriteString(span.Text)
				}
			}
		}
		for _, span := range block.Spans {
			out.WriteString(span.Text)
		}
	}
	return out.String()
}

// Rows already fit the shared display-width measurer. Painting only visible
// rows keeps terminal styles out of parsing, wrapping and the cached source.
func (s Styles) markdownRow(row render.StyledRow) string {
	var out strings.Builder
	for _, span := range row.Spans {
		style := s.Text
		if span.Style.Muted {
			style = s.Muted
		}
		if span.Style.Code {
			style = s.Code
			if s.options.Profile != Monochrome && s.options.Theme != "high-contrast" {
				switch span.Style.Syntax {
				case render.SyntaxText:
				case render.SyntaxAccent:
					style = style.Foreground(s.Accent.GetForeground())
				case render.SyntaxMuted:
					style = style.Foreground(s.Muted.GetForeground())
				}
			}
		}
		if span.Style.Link {
			style = s.Accent
		}
		if s.options.Profile != Monochrome {
			style = style.Bold(span.Style.Bold).Italic(span.Style.Italic).Underline(span.Style.Link)
		}
		value := style.Render(span.Text)
		if span.Style.URL != "" && s.options.Profile != Monochrome {
			value = ansi.SetHyperlink(span.Style.URL) + value + ansi.ResetHyperlink()
		}
		out.WriteString(value)
	}
	return out.String()
}
