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

// Rows already fit the shared display-width measurer. Painting only visible
// rows keeps terminal styles out of parsing, wrapping and the cached source.
func (s Styles) markdownRow(row render.StyledRow) string {
	var out strings.Builder
	for _, span := range row.Spans {
		style := s.Text
		if span.Style.Code {
			style = s.Code
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
