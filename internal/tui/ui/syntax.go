// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/thisisjun786/lina/internal/tui/render"
)

// Lex only explicitly named, closed fences. Chroma supplies token roles, not
// terminal escapes or a separate palette. Keep malformed/unknown code readable.
func highlightCode(code, language string) []render.Span {
	plain := []render.Span{{Text: code, Style: render.InlineStyle{Code: true}}}
	if language == "" {
		return plain
	}
	lexer := lexers.Get(language)
	if lexer == nil {
		return plain
	}
	iterator, err := lexer.Tokenise(nil, code)
	if err != nil {
		return plain
	}
	var spans []render.Span
	offset, start := 0, 0
	for token := iterator(); token != chroma.EOF; token = iterator() {
		tone := render.SyntaxText
		switch {
		case token.Type.InCategory(chroma.Comment):
			tone = render.SyntaxMuted
		case token.Type.InCategory(chroma.Keyword), token.Type.InSubCategory(chroma.LiteralString), token.Type.InSubCategory(chroma.LiteralNumber), token.Type == chroma.NameBuiltin, token.Type == chroma.NameBuiltinPseudo, token.Type == chroma.NameFunction, token.Type == chroma.NameClass:
			tone = render.SyntaxAccent
		}
		end := offset + len(token.Value)
		if end > len(code) || code[offset:end] != token.Value {
			return plain
		}
		offset = end
		style := render.InlineStyle{Code: true, Syntax: tone}
		if len(spans) > 0 && spans[len(spans)-1].Style == style {
			spans[len(spans)-1].Text = code[start:offset]
		} else {
			start = offset - len(token.Value)
			spans = append(spans, render.Span{Text: token.Value, Style: style})
		}
	}
	// A lexer must not normalize whitespace or add/drop source bytes.
	if offset != len(code) {
		return plain
	}
	return spans
}
